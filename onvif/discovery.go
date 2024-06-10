package onvif

import (
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/net/ipv4"
)

// From https://www.onvif.org/wp-content/uploads/2021/01/ONVIF_Device_Feature_Discovery_Specification_20.12.pdf
const probeTemplate = `<?xml version="1.0" ?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
	<s:Header xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing">
		<a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action>
		<a:MessageID>urn:uuid:{{UUID}}</a:MessageID>
		<a:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
	</s:Header>
	<s:Body>
		<d:Probe xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery">
			<d:Types />
			<d:Scopes />
		</d:Probe>
	</s:Body>
</s:Envelope>`

// <?xml version=\"1.0\" encoding=\"UTF-8\" ?>
// <s:Envelope xmlns:s=\"http://www.w3.org/2003/05/soap-envelope\" xmlns:sc=\"http://www.w3.org/2003/05/soap-encoding\" xmlns:d=\"http://schemas.xmlsoap.org/ws/2005/04/discovery\" xmlns:a=\"http://schemas.xmlsoap.org/ws/2004/08/addressing\" xmlns:dn=\"http://www.onvif.org/ver10/network/wsdl\" xmlns:tds=\"http://www.onvif.org/ver10/device/wsdl\">
// <s:Header>
//
//	 <a:MessageID>uuid:6f3f15ac-9f75-9eb4-697b-26774d859f75</a:MessageID>
//	 <a:To>urn:schemas-xmlsoap-org:ws:2005:04:discovery</a:To>
//	 <a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</a:Action>
//	 <a:RelatesTo>urn:uuid:eccb3c9d-8031-4215-a1f6-b25e8efa52a6</a:RelatesTo>
//	 </s:Header>
//	 <s:Body>
//	 <d:ProbeMatches>
//	   <d:ProbeMatch>
//	     <a:EndpointReference>
//	       <a:Address>uuid:b1fc8184-b342-a2b0-8db5-cc7447feb342</a:Address>
//	     </a:EndpointReference>
//	     <d:Types>dn:NetworkVideoTransmitter tds:Device</d:Types>
//	     <d:Scopes>onvif://www.onvif.org/location/country/china onvif://www.onvif.org/name/Amcrest onvif://www.onvif.org/hardware/IP5M-T1179E onvif://www.onvif.org/Profile/Streaming onvif://www.onvif.org/type/Network_Video_Transmitter onvif://www.onvif.org/extension/unique_identifier onvif://www.onvif.org/Profile/T</d:Scopes>
//	     <d:XAddrs>http://192.168.10.108/onvif/device_service</d:XAddrs>
//	     <d:MetadataVersion>1</d:MetadataVersion>
//	   </d:ProbeMatch>
//	</d:ProbeMatches>
//
// </s:Body></s:Envelope>
type ProbeResponse struct {
	UUID    string `xml:"Header>RelatesTo"`
	Matches []struct {
		EndpointReference string `xml:"EndpointReference>Address"`
		Types             string `xml:"Types"`
		Scopes            string `xml:"Scopes"`
		XAddrs            string `xml:"XAddrs"`
	} `xml:"Body>ProbeMatches>ProbeMatch"`
}

func GetONVIFVideoTransmitters(log *slog.Logger, ifaceName string) ([]string, error) {
	log = log.With("iface", ifaceName)

	// build our message
	msgID := uuid.NewString()
	msg := strings.ReplaceAll(probeTemplate, "{{UUID}}", msgID)

	log = log.With("msgID", msgID)

	// start listening for responses before sending our probe
	c, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("unable to starting discovery listen: %w", err)
	}
	defer c.Close()

	group := net.IPv4(239, 255, 255, 250)
	dest := &net.UDPAddr{IP: group, Port: 3702}

	p := ipv4.NewPacketConn(c)
	iface, err := net.InterfaceByName(string(ifaceName))
	if err != nil {
		return nil, fmt.Errorf("unable to find interface %q: %w", ifaceName, err)
	}

	err = p.JoinGroup(iface, &net.UDPAddr{IP: group})
	if err != nil {
		return nil, fmt.Errorf("interface %q unable to join multicast group: %w", ifaceName, err)
	}

	err = p.SetMulticastInterface(iface)
	if err != nil {
		return nil, fmt.Errorf("interface %q unable to set multicast interface: %w", ifaceName, err)
	}

	p.SetMulticastTTL(3)

	_, err = p.WriteTo([]byte(msg), nil, dest)
	if err != nil {
		return nil, fmt.Errorf("unable to send discovery probe on interface %q: %w", ifaceName, err)
	}

	if err = p.SetReadDeadline(time.Now().Add(time.Second * 3)); err != nil {
		return nil, fmt.Errorf("unable to set read deadline: %w", err)
	}

	transmitters := []string{}

	b := make([]byte, 32768)
	for {
		n, _, src, err := p.ReadFrom(b)

		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				break
			} else {
				log.Error("error reading discovery response", err)
				return nil, fmt.Errorf("error reading discovery response: %w", err)
			}
		}

		log.Debug("discovery response", slog.String("src", src.String()), slog.String("msg", string(b[:n])))

		var resp ProbeResponse
		err = xml.Unmarshal(b[:n], &resp)
		if err != nil {
			log.Warn("error unmarshalling discovery response, skipping", slog.String("msg", string(b[:n])), slog.String("error", err.Error()))
			continue
		}

		// ignore responses that don't match our probe
		if !strings.Contains(resp.UUID, msgID) {
			log.Warn("discovery response does not match probe, ignoring", slog.String("uuid", resp.UUID))
			continue
		}

		// run through our matches looking for one that streams
		for _, match := range resp.Matches {
			if strings.Contains(match.Types, "NetworkVideoTransmitter") {
				// replace the IP with the source IP (some cameras return the wrong one)
				endpoint, err := url.Parse(match.XAddrs)
				if err != nil {
					log.Warn("error parsing xaddrs, skipping",
						slog.String("xaddrs", match.XAddrs),
						slog.String("error", err.Error()))
					continue
				}

				// default to port 80 if not specified
				port := endpoint.Port()
				if port == "" {
					port = "80"
				}

				endpoint.Host = fmt.Sprintf("%s:%s", ipFromAddr(src), port)

				log.Info("discovered onvif video transmitter",
					slog.String("endpoint", endpoint.String()),
					slog.String("scopes", match.Scopes))

				transmitters = append(transmitters, endpoint.String())
			}
		}
	}

	return transmitters, nil
}

func ipFromAddr(addr net.Addr) string {
	parts := strings.Split(addr.String(), ":")
	return parts[0]
}
