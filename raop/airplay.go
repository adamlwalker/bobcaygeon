package raop

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/grandcat/zeroconf"
	"github.com/nstehr/bobcaygeon/player"
	"github.com/nstehr/bobcaygeon/rtsp"
	"github.com/nstehr/bobcaygeon/sdp"
)

// sets up the properties needed to make us discoverable as a airtunes service
// https://github.com/fgp/AirReceiver/blob/STABLE_1_X/src/main/java/org/phlo/AirReceiver/AirReceiver.java#L88
// https://nto.github.io/AirPlay.html#audio
const (
	airTunesServiceType = "_raop._tcp"
	domain              = "local."
	localTimingPort     = 6002
	localControlPort    = 6001
)

var airtunesServiceProperties = []string{"txtvers=1",
	"tp=UDP",
	"ch=2",
	"ss=16",
	"sr=44100",
	"pw=false",
	"sm=false",
	"sv=false",
	"ek=1",
	"et=0,1",
	"cn=0,1",
	"md=0,1,2",
	"vn=3"}

// AirplayServer server for handling the RTSP protocol
type AirplayServer struct {
	port          int
	dataPort      int
	name          string
	rtspServer    *rtsp.Server
	zerconfServer *zeroconf.Server
	session       *rtsp.Session
	player        player.Player
}

// NewAirplayServer instantiates a new airplayer server
func NewAirplayServer(port int, dataPort int, name string, player player.Player) *AirplayServer {
	as := AirplayServer{port: port, dataPort: dataPort, name: name, player: player}
	return &as
}

//Start starts the airplay server, broadcasting on bonjour, ready to accept requests
func (a *AirplayServer) Start(verbose bool, advertise bool) {

	if advertise {
		a.initAdvertise()
	}

	rtspServer := rtsp.NewServer(a.port)

	a.rtspServer = rtspServer

	rtspServer.AddHandler(rtsp.Options, handleOptions)
	rtspServer.AddHandler(rtsp.Announce, a.handleAnnounce)
	rtspServer.AddHandler(rtsp.Setup, a.handleSetup)
	rtspServer.AddHandler(rtsp.Record, a.handleRecord)
	rtspServer.AddHandler(rtsp.Set_Parameter, a.handlSetParameter)
	rtspServer.AddHandler(rtsp.Flush, handlFlush)
	rtspServer.AddHandler(rtsp.Teardown, a.handleTeardown)
	rtspServer.Start(verbose)

}

// ToggleAdvertise will toggle whether or not to advertise as an airplay service
func (a *AirplayServer) ToggleAdvertise(shouldAdvertise bool) {
	if !shouldAdvertise {
		if a.zerconfServer == nil {
			log.Println("Currently not advertising, ignoring turn off advertise request")
			return
		}
		// if we have a zerconfServer reference it means we are already advertising, so
		// stop it
		log.Printf("Shutting down broadcasting of %s\n", a.name)
		a.zerconfServer.Shutdown()
		a.zerconfServer = nil

	} else {
		if a.zerconfServer != nil {
			log.Println("Currently advertising, ignoring turn on advertise request")
			return
		}
		a.initAdvertise()
	}
}

//ChangeName will change the name of the broadcast service
func (a *AirplayServer) ChangeName(newName string) error {
	if strings.TrimSpace(newName) == "" {
		return errors.New("New name must be non-empty")
	}
	a.name = strings.TrimSpace(newName)
	// if we are advertising, stop the zeroconf server and start it so it
	// reflects the name change
	if a.zerconfServer != nil {
		a.zerconfServer.Shutdown()
		a.zerconfServer = nil
		a.initAdvertise()
	}
	return nil
}

func (a *AirplayServer) initAdvertise() {
	// as per the protocol, the mac address makes up part of the service name
	macAddr := getMacAddr().String()
	macAddr = strings.Replace(macAddr, ":", "", -1)

	serviceName := fmt.Sprintf("%s@%s", macAddr, a.name)

	server, err := zeroconf.Register(serviceName, airTunesServiceType, domain, a.port, airtunesServiceProperties, nil)
	if err != nil {
		log.Fatal("couldn't start zeroconf: ", err)
	}

	log.Println("Published service:")
	log.Println("- Name:", serviceName)
	log.Println("- Type:", airTunesServiceType)
	log.Println("- Domain:", domain)
	log.Println("- Port:", a.port)

	a.zerconfServer = server
}

func handleOptions(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	resp.Status = rtsp.Ok
	resp.Headers["Public"] = strings.Join(rtsp.GetMethods(), " ")
	appleChallenge, exists := req.Headers["Apple-Challenge"]
	if !exists {
		return
	}
	log.Printf("Apple Challenge detected: %s\n", appleChallenge)
	challengResponse, err := generateChallengeResponse(appleChallenge, getMacAddr(), localAddress)
	if err != nil {
		log.Println("Error generating challenge response: ", err.Error())
	}
	resp.Headers["Apple-Response"] = challengResponse

}

func (a *AirplayServer) handleAnnounce(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	if req.Headers["Content-Type"] == "application/sdp" {
		description, err := sdp.Parse(bytes.NewReader(req.Body))
		if err != nil {
			log.Println("error parsing SDP payload: ", err)
			resp.Status = rtsp.BadRequest
			return
		}

		// right now, we only maintain one audio session, so close any existing one
		if a.session != nil {
			a.session.Close()
		}
		var decoder rtsp.Decrypter

		if key, ok := description.Attributes["rsaaeskey"]; ok {
			aesKey, err := aeskeyFromRsa(key)
			if err != nil {
				log.Println("error retrieving aes key", err)
				resp.Status = rtsp.InternalServerError
				return
			}
			// from: https://github.com/joelgibson/go-airplay/blob/19e70c97e3903365f0a7f5a3f3c33751f4e8fb94/airplay/rtsp.go#L149
			aesIv64 := description.Attributes["aesiv"]
			aesIv64 = base64pad(aesIv64)
			aesIv, err := base64.StdEncoding.DecodeString(aesIv64)
			if err != nil {
				log.Println("error retrieving aes IV", err)
				resp.Status = rtsp.InternalServerError
				return
			}
			decoder = NewAesDecrypter(aesKey, aesIv)
		}
		a.session = rtsp.NewSession(description, decoder)
	}
	resp.Status = rtsp.Ok
}

func (a *AirplayServer) handleSetup(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	transport, hasTransport := req.Headers["Transport"]
	if hasTransport {
		transportParts := strings.Split(transport, ";")
		var controlPort int
		var timingPort int
		for _, part := range transportParts {
			if strings.Contains(part, "control_port") {
				controlPort, _ = strconv.Atoi(strings.Split(part, "=")[1])
			}
			if strings.Contains(part, "timing_port") {
				timingPort, _ = strconv.Atoi(strings.Split(part, "=")[1])
			}
		}
		a.session.RemotePorts.Address = remoteAddress
		a.session.RemotePorts.Control = controlPort
		a.session.RemotePorts.Timing = timingPort
	}

	// hardcode our listening ports for now
	a.session.LocalPorts.Control = localControlPort
	a.session.LocalPorts.Timing = localTimingPort
	a.session.LocalPorts.Data = a.dataPort

	resp.Headers["Transport"] = fmt.Sprintf("RTP/AVP/UDP;unicast;mode=record;server_port=%d;control_port=%d;timing_port=%d", a.dataPort, localControlPort, localTimingPort)
	resp.Headers["Session"] = "1"
	resp.Headers["Audio-Jack-Status"] = "connected"
	resp.Status = rtsp.Ok
}

func (a *AirplayServer) handleRecord(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	err := a.session.StartReceiving()
	if err != nil {
		log.Println("could not start streaming session: ", err)
		resp.Status = rtsp.InternalServerError
		return
	}
	a.player.Play(a.session)
	resp.Headers["Audio-Latency"] = "2205"
	resp.Status = rtsp.Ok

}

func (a *AirplayServer) handlSetParameter(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	if req.Headers["Content-Type"] == "application/x-dmap-tagged" {
		parseDaap(req.Body)
	} else if req.Headers["Content-Type"] == "image/jpeg" {
		go func(data []byte) {
			err := ioutil.WriteFile("img.jpg", data, 0644)
			if err != nil {
				log.Println("Couldn't save album art", err)
			}
		}(req.Body)

	} else if req.Headers["Content-Type"] == "text/parameters" {
		body := string(req.Body)
		if strings.Contains(body, "volume") {
			volStr := strings.TrimSpace(strings.Split(body, "volume:")[1])
			vol, err := strconv.ParseFloat(volStr, 32)
			if err != nil {
				log.Println("Error converting volume to float: ", err)
				resp.Status = rtsp.BadRequest
				return
			}
			vol = normalizeVolume(vol)
			a.player.SetVolume(vol)
		}
	}
	resp.Status = rtsp.Ok
}

func handlFlush(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	resp.Status = rtsp.Ok
}

func (a *AirplayServer) handleTeardown(req *rtsp.Request, resp *rtsp.Response, localAddress string, remoteAddress string) {
	if a.session != nil {
		a.session.Close()
	}
	resp.Status = rtsp.Ok
}

// Stop stops thes airplay server
func (a *AirplayServer) Stop() {
	if a.session != nil {
		a.session.Close()
	}
	a.rtspServer.Stop()
	if a.zerconfServer != nil {
		a.zerconfServer.Shutdown()
	}

}

// getMacAddr gets the MAC hardware
// address of the host machine: https://gist.github.com/rucuriousyet/ab2ab3dc1a339de612e162512be39283
func getMacAddr() (addr net.HardwareAddr) {
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, i := range interfaces {
			if i.Flags&net.FlagUp != 0 && bytes.Compare(i.HardwareAddr, nil) != 0 {
				// Don't use random as we have a real address
				addr = i.HardwareAddr
				break
			}
		}
	}
	return
}

// normalizeVolume maps airplay volume values to a range betweeon 0 and 1
func normalizeVolume(volume float64) float64 {
	// according to: https://nto.github.io/AirPlay.html#audio
	// -144 is mute
	if volume == -144 {
		return 0
	}
	if volume == 0 {
		return 1
	}
	// the remaining values will between -30 and 0,
	// so map that to a range between 0 and 1
	// simple range mapping formula: https://gamedev.stackexchange.com/questions/33441/how-to-convert-a-number-from-one-min-max-set-to-another-min-max-set
	// then simplified down and adjusted to make sure the number was positive
	adjusted := (volume + 30) / 30
	return adjusted
}
