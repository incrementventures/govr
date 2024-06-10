package onvif

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/incrementventures/govr/ffmpeg"
	"github.com/nyaruka/gocommon/httpx"
)

type Device struct {
	Address  string
	Username string
	Password string

	// cameras often have a clock that is off by some amount which then causes auth to fail, this is the offset
	// to apply from our system clock to the camera clock to account for that
	ClockOffset time.Duration

	Capabilities      Capabilities
	DeviceInformation DeviceInformation
	Profiles          []Profile
	MediaProfiles     []MediaProfile
}

type MediaProfile struct {
	Token         string
	URI           string
	SourceStreams []Stream
	RTSPStreams   []ffmpeg.Stream
}

type DeviceInformation struct {
	Manufacturer    string `xml:"Body>GetDeviceInformationResponse>Manufacturer"`
	Model           string `xml:"Body>GetDeviceInformationResponse>Model"`
	FirmwareVersion string `xml:"Body>GetDeviceInformationResponse>FirmwareVersion"`
	SerialNumber    string `xml:"Body>GetDeviceInformationResponse>SerialNumber"`
	HardwareID      string `xml:"Body>GetDeviceInformationResponse>HardwareID"`
}

type Stream struct {
	Index         int    `json:"index"`
	CodecType     string `json:"codec_type"`
	CodecName     string `json:"codec_name"`
	CodecLongName string `json:"codec_long_name"`
	Width         int    `json:"width"`
	Height        int    `json:"height"`
	FrameRate     string `json:"avg_frame_rate"`
}

type GetStreamUriResponse struct {
	MediaURI struct {
		URI                 string `xml:"Uri"`
		InvalidAfterConnect bool   `xml:"InvalidAfterConnect"`
		InvalidAfterReboot  bool   `xml:"InvalidAfterReboot"`
		Timeout             string `xml:"Timeout"`
	} `xml:"Body>GetStreamUriResponse>MediaUri"`
}

// <s:Envelope xmlns:sc=\"http://www.w3.org/2003/05/soap-encoding\" xmlns:s=\"http://www.w3.org/2003/05/soap-envelope\" xmlns:tt=\"http://www.onvif.org/ver10/schema\" xmlns:tds=\"http://www.onvif.org/ver10/device/wsdl\">
// <s:Header/>
// <s:Body>
//
//	<tds:Capabilities>
//	  <tds:Capabilities>
//	    <tt:Analytics>..</tt:Analytics>
//	    <tt:Device>..</tt:Device>
//	    <tt:Events>
//	      <tt:XAddr>http://192.168.10.108/onvif/event_service</tt:XAddr>
//	      <tt:WSSubscriptionPolicySupport>true</tt:WSSubscriptionPolicySupport>
//	      <tt:WSPullPointSupport>true</tt:WSPullPointSupport>
//	      <tt:WSPausableSubscriptionManagerInterfaceSupport>false</tt:WSPausableSubscriptionManagerInterfaceSupport>
//	    </tt:Events>
//	    <tt:Imaging>
//	      <tt:XAddr>http://192.168.10.108/onvif/imaging_service</tt:XAddr>
//	    </tt:Imaging>
//	    <tt:Media>
//	      <tt:XAddr>http://192.168.10.108/onvif/media_service</tt:XAddr>
//	      <tt:StreamingCapabilities>
//	        <tt:RTPMulticast>true</tt:RTPMulticast>
//	        <tt:RTP_TCP>true</tt:RTP_TCP>
//	        <tt:RTP_RTSP_TCP>true</tt:RTP_RTSP_TCP>
//	      </tt:StreamingCapabilities>
//	      <tt:Extension>
//	        <tt:ProfileCapabilities>
//	          <tt:MaximumNumberOfProfiles>6</tt:MaximumNumberOfProfiles>
//	        </tt:ProfileCapabilities>
//	      </tt:Extension>
//	    </tt:Media>
//	    <tt:Extension>..</tt:Extension>
//
// </s:Body>
// </s:Envelope>
type Capabilities struct {
	Events struct {
		Address                                       string `xml:"XAddr"`
		WSSubscriptionPolicySupport                   bool   `xml:"WSSubscriptionPolicySupport"`
		WSPullPointSupport                            bool   `xml:"WSPullPointSupport"`
		WSPausableSubscriptionManagerInterfaceSupport bool   `xml:"WSPausableSubscriptionManagerInterfaceSupport"`
	} `xml:"Body>GetCapabilitiesResponse>Capabilities>Events"`
	Media struct {
		Address string `xml:"XAddr"`
	} `xml:"Body>GetCapabilitiesResponse>Capabilities>Media"`
}

type GetProfileResponse struct {
	Profiles []Profile `xml:"Body>GetProfilesResponse>Profiles"`
}

type Profile struct {
	Token                    string `xml:"token,attr"`
	Name                     string `xml:"Name"`
	URI                      string
	VideoSourceConfiguration struct {
		Bounds struct {
			Width  int `xml:"width,attr"`
			Height int `xml:"height,attr"`
		} `xml:"Bounds"`
	} `xml:"VideoSourceConfiguration"`
	Streams []ffmpeg.Stream
}

type GetSystemDateAndTimeResponse struct {
	UTCDateTime struct {
		Time struct {
			Hour   int `xml:"Hour"`
			Minute int `xml:"Minute"`
			Second int `xml:"Second"`
		}
		Date struct {
			Year  int `xml:"Year"`
			Month int `xml:"Month"`
			Day   int `xml:"Day"`
		}
	} `xml:"Body>GetSystemDateAndTimeResponse>SystemDateAndTime>UTCDateTime"`
}

func NewDevice(address string, username string, password string) *Device {
	return &Device{
		Address: address,

		Username: username,
		Password: password,
	}
}

const getStreamUriBody = `
<trt:GetStreamUri xmlns:trt="http://www.onvif.org/ver10/media/wsdl" xmlns:tt="http://www.onvif.org/ver10/schema">
	<trt:StreamSetup>
		<tt:Stream>RTP-Unicast</tt:Stream>
		<tt:Transport><tt:Protocol>RTSP</tt:Protocol></tt:Transport>
	</trt:StreamSetup>
	<trt:ProfileToken>{{token}}</trt:ProfileToken>
</trt:GetStreamUri>`

const getProfilesBody = `<trt:GetProfiles xmlns:trt="http://www.onvif.org/ver10/media/wsdl"/>`

func (d *Device) GetProfiles(log *slog.Logger) ([]Profile, error) {
	resp := &GetProfileResponse{}
	_, err := d.makeRequest(log, d.Capabilities.Media.Address, getProfilesBody, resp)
	if err != nil {
		return nil, err
	}

	// for each profile, populate the stream information
	profiles := resp.Profiles
	for i, profile := range profiles {
		body := strings.ReplaceAll(getStreamUriBody, "{{token}}", profile.Token)
		uri := &GetStreamUriResponse{}
		_, err := d.makeRequest(log, d.Capabilities.Media.Address, body, uri)
		if err != nil {
			return nil, err
		}
		profiles[i].URI = uri.MediaURI.URI
	}

	log.Debug("got profiles", slog.String("response", fmt.Sprintf("%+v", profiles)))
	return profiles, nil
}

func (d *Device) Probe(log *slog.Logger) (bool, error) {
	capabilities, err := d.GetCapabilities(log)
	if err != nil {
		return false, err
	}

	// if we don't have a media address, we aren't useful
	if capabilities.Media.Address == "" {
		return false, fmt.Errorf("no media address found in capabilities")
	}
	d.Capabilities = *capabilities

	// first get our clock offset so we can make auth calls
	deviceTime, err := d.GetSystemDateAndTime(log)
	if err != nil {
		return false, err
	}

	d.ClockOffset = -time.Since(deviceTime)

	// then get our device information
	info, err := d.GetDeviceInformation(log)
	if err != nil {
		return true, err
	}
	d.DeviceInformation = *info

	// then get our media profiles
	profiles, err := d.GetProfiles(log)
	if err != nil {
		return true, err
	}
	d.Profiles = profiles

	return true, nil
}

const getCapabilitiesBody = `
<tds:GetCapabilities xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
	<tds:Category>All</tds:Category>
</tds:GetCapabilities>`

func (d *Device) GetCapabilities(log *slog.Logger) (*Capabilities, error) {
	capabilities := &Capabilities{}
	_, err := d.makeRequest(log, d.Address, getCapabilitiesBody, capabilities)
	if err != nil {
		return nil, fmt.Errorf("failed to get capabilities: %w", err)
	}

	log.Debug("got capabilities", slog.String("response", fmt.Sprintf("%+v", capabilities)))
	return capabilities, nil
}

const getDateAndTimeBody = `<tds:GetSystemDateAndTime xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`

func (d *Device) GetSystemDateAndTime(log *slog.Logger) (time.Time, error) {
	dt := &GetSystemDateAndTimeResponse{}
	_, err := d.makeRequest(log, d.Address, getDateAndTimeBody, dt)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get system date and time: %w", err)
	}

	deviceUTCTime := time.Date(
		dt.UTCDateTime.Date.Year,
		time.Month(dt.UTCDateTime.Date.Month),
		dt.UTCDateTime.Date.Day,
		dt.UTCDateTime.Time.Hour,
		dt.UTCDateTime.Time.Minute,
		dt.UTCDateTime.Time.Second,
		0,
		time.UTC,
	)

	log.Debug("got system date and time", slog.String("response", fmt.Sprintf("%+v", dt.UTCDateTime)))
	return deviceUTCTime, nil
}

const getDeviceInformationBody = `<tds:GetDeviceInformation xmlns:tds="http://www.onvif.org/ver10/device/wsdl"/>`

func (d *Device) GetDeviceInformation(log *slog.Logger) (*DeviceInformation, error) {
	info := &DeviceInformation{}
	trace, err := d.makeRequest(log, d.Address, getDeviceInformationBody, info)
	if err != nil {
		log.Error("failed to get device information", slog.String("trace", trace.String()), slog.String("error", err.Error()))
		return nil, fmt.Errorf("failed to get device information: %w", err)
	}

	log.Debug("got device information", slog.String("response", fmt.Sprintf("%+v", info)))
	return info, nil
}

const envelopeTemplate = `
<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
{{header}}
<s:Body>{{body}}</s:Body>
</s:Envelope>`

const authTemplate = `<s:Header>
<wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd">
<wsse:UsernameToken>
<wsse:Username>{{username}}</wsse:Username>
<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">{{password}}</wsse:Password>
<wsse:Nonce EncodingType="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary">{{nonce}}</wsse:Nonce>
<wsu:Created xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd">{{created}}</wsu:Created>
</wsse:UsernameToken>
</wsse:Security>
</s:Header>`

var accessPolicy = httpx.NewAccessConfig(time.Second*5, []net.IP{}, []*net.IPNet{})
var retryPolicy = httpx.NewFixedRetries(1*time.Second, 1*time.Second, 1*time.Second)

func (d *Device) makeRequest(log *slog.Logger, url string, body string, resp interface{}) (*httpx.Trace, error) {
	buf := bytes.NewBuffer(nil)

	header := ""
	if d.Username != "" {
		nonce := uuid.NewString()
		created := time.Now().Add(d.ClockOffset).UTC().Format(time.RFC3339Nano)

		hash := sha1.New()
		hash.Write([]byte(nonce + created + d.Password))

		header = strings.ReplaceAll(authTemplate, "{{username}}", d.Username)
		header = strings.ReplaceAll(header, "{{nonce}}", base64.StdEncoding.EncodeToString([]byte(nonce)))
		header = strings.ReplaceAll(header, "{{password}}", base64.StdEncoding.EncodeToString(hash.Sum(nil)))
		header = strings.ReplaceAll(header, "{{created}}", created)
	}

	envelope := strings.ReplaceAll(envelopeTemplate, "{{header}}", header)
	envelope = strings.ReplaceAll(envelope, "{{body}}", body)
	buf.WriteString(envelope)

	req, err := httpx.NewRequest(http.MethodPost, url, buf, map[string]string{"Content-Type": "application/soap+xml;charset=utf-8"})
	if err != nil {
		return nil, fmt.Errorf("failed to create request for url %q: %w", url, err)
	}

	trace, err := httpx.DoTrace(http.DefaultClient, req, retryPolicy, accessPolicy, 1024*1024)
	log.Debug("onvif request", slog.String("url", url), slog.String("trace", trace.String()))
	if err != nil {
		return trace, fmt.Errorf("failed to make request to URL %q: %w", url, err)
	}

	if trace.Response.StatusCode != http.StatusOK {
		return trace, fmt.Errorf("non 200 status %d for %q", trace.Response.StatusCode, url)
	}

	err = xml.Unmarshal(trace.ResponseBody, resp)
	if err != nil {
		return trace, fmt.Errorf("failed to unmarshal response %q: %w", trace.ResponseBody[:128], err)
	}

	return trace, nil
}
