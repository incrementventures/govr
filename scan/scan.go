package scan

import (
	"fmt"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/incrementventures/govr/ffmpeg"
	"github.com/incrementventures/govr/network"
	"github.com/incrementventures/govr/onvif"
	"github.com/sourcegraph/conc"
)

func GetDevicesOnNetwork(log *slog.Logger, port int, username string, password string) ([]onvif.Device, error) {
	// get all private IP4 interfaces
	ifaces, err := network.GetPrivateIP4Interfaces()
	if err != nil {
		return nil, fmt.Errorf("error getting IP4 interfaces: %w", err)
	}

	// first use ws-discovery to find ONVIF devices
	candidates := []string{}
	for iface := range ifaces {
		log.Info("starting onvif ws-discovery", slog.Any("iface", iface))
		ifaceCandidates, err := onvif.GetONVIFVideoTransmitters(log, string(iface))
		if err != nil {
			return nil, fmt.Errorf("error finding candidates via ws discovery: %w", err)
		}
		for _, candidate := range ifaceCandidates {
			candidates = append(candidates, candidate)
		}
		log.Info("onvif ws-discovery complete", slog.Any("iface", iface), slog.Int("count", len(ifaceCandidates)))
	}

	// then do a port scan to find anything with our port open
	log.Info("starting ip scanning", slog.Int("port", port))
	portCandidates, err := FindHostsWithOpenPort(log, ifaces, port)
	if err != nil {
		return nil, fmt.Errorf("error finding candidates via scan: %w", err)
	}
	for _, candidate := range portCandidates {
		candidates = append(candidates, fmt.Sprintf("http://%s/onvif/device_service", candidate))
	}
	log.Info("ip scanning complete", slog.Int("port", port), slog.Int("count", len(portCandidates)))

	seen := make(map[string]bool)

	// for each candidate see if it is an ONVIF device
	for _, candidate := range candidates {
		// we've already seen this candidate
		if seen[candidate] {
			continue
		}
		seen[candidate] = true

		// check if it is an ONVIF device
		d := onvif.NewDevice(candidate, username, password)
		valid, err := d.Probe(log)
		if err != nil {
			log.Debug("error probing onvif device, ignoring", slog.String("candidate", candidate), slog.String("error", err.Error()))
			continue
		}
		if !valid {
			log.Debug("not a valid onvif device, ignoring", slog.String("candidate", candidate))
			continue
		}

		for i, profile := range d.Profiles {
			uri, _ := url.Parse(profile.URI)
			if d.Username != "" {
				uri.User = url.UserPassword(d.Username, d.Password)
			}
			streams, err := ffmpeg.ProbeRTSP(log, uri.String())
			if err != nil {
				log.Debug("unable to open RTSP stream", slog.String("url", profile.URI))
				continue
			}
			log.Info("rtsp stream", slog.String("url", profile.URI), slog.String("profile", fmt.Sprintf("%+v", streams)))
			d.Profiles[i].Streams = streams
		}

		log.Info("onvif device found",
			slog.String("address", d.Address),
			slog.String("manufacturer", d.DeviceInformation.Manufacturer),
			slog.String("model", d.DeviceInformation.Model),
			slog.String("firmware", d.DeviceInformation.FirmwareVersion),
			slog.String("serial", d.DeviceInformation.SerialNumber),
			slog.String("hardware", d.DeviceInformation.HardwareID),
			slog.String("profiles", fmt.Sprintf("%+v", d.Profiles)))
	}

	return nil, nil
}

func FindHostsWithOpenPort(log *slog.Logger, ifaces map[network.IFace]network.CIDR, port int) ([]string, error) {
	// map of address candidates to scan
	candidates := make(map[string]bool)

	// for each interface, get all candidate IPs
	for iface, ip := range ifaces {
		ips, err := network.GetIPsOnNetwork(ip)
		if err != nil {
			return nil, fmt.Errorf("error getting IPs for interface %q: %w", iface, err)
		}
		if len(ips) <= 256 {
			for _, ip := range ips {
				candidates[fmt.Sprintf("%s:%d", ip, port)] = true
			}
			log.Info("scanning candidate IPs on interface",
				slog.Any("interface", iface),
				slog.Any("cidr", ip),
				slog.Int("count", len(ips)))

		} else {
			log.Info("ignoring interface with too many IPs",
				slog.Any("interface", iface),
				slog.Any("cidr", ip),
				slog.Int("count", len(ips)))
		}
	}

	wg := conc.WaitGroup{}
	keepers := []string{}
	mu := sync.Mutex{}

	for candidate := range candidates {
		wg.Go(func() {
			open, err := network.IsPortOpen(candidate, 50*time.Millisecond)
			if err != nil {
				log.Error("error checking port", slog.String("candidate", candidate), slog.String("error", err.Error()))
				panic(err)
			}
			if open {
				log.Info("found open port", slog.String("candidate", candidate))
				mu.Lock()
				keepers = append(keepers, candidate)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return keepers, nil
}
