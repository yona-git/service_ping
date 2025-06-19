package ping

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"ping-monitor/models"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

const (
	maxPingAttempts = 5
	logPrefix       = "PING: "
)

var (
	idCounter uint32
	idMu      sync.Mutex
)

func getNextID() uint16 {
	return uint16(atomic.AddUint32(&idCounter, 1) & 0xffff
}

func PingServer(server *models.Server, wg *sync.WaitGroup, mu *sync.Mutex, timeout time.Duration) {
	defer wg.Done()

	ipAddr, err := net.ResolveIPAddr("ip4", server.IP)
	if err != nil {
		log.Printf("%sServer %s (%s) - Resolve error: %v", logPrefix, server.Name, server.IP, err)
		setServerStatus(server, "error", mu)
		return
	}

	netconn, err := net.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		log.Printf("%sServer %s (%s) - ICMP listen error: %v", logPrefix, server.Name, server.IP, err)
		setServerStatus(server, "error", mu)
		return
	}
	defer netconn.Close()

	id := getNextID()
	seq := rand.Intn(1000) + 1

	var isAlive bool
	for attempt := 0; attempt < maxPingAttempts; attempt++ {
		log.Printf("%sServer %s (%s) - Attempt %d/%d (ID: %d, Seq: %d)", 
			logPrefix, server.Name, server.IP, attempt+1, maxPingAttempts, id, seq)

		isAlive = pingAttempt(netconn, ipAddr, timeout, server.Name, server.IP, id, seq)
		if isAlive {
			log.Printf("%sServer %s (%s) - PING SUCCESS after attempt %d", 
				logPrefix, server.Name, server.IP, attempt+1)
			break
		}
		
		if attempt < maxPingAttempts-1 {
			time.Sleep(100 * time.Millisecond)
		}
		seq++
	}

	newStatus := "dead"
	if isAlive {
		newStatus = "alive"
	}
	setServerStatus(server, newStatus, mu)
	log.Printf("%sServer %s (%s) - Status changed to %s", 
		logPrefix, server.Name, server.IP, newStatus)
}

func pingAttempt(netconn net.PacketConn, ipAddr *net.IPAddr, timeout time.Duration, 
	serverName, serverIP string, id uint16, seq int) bool {
	
	err := netconn.SetReadDeadline(time.Now().Add(timeout))
	if err != nil {
		log.Printf("%sServer %s (%s) - Set read deadline error: %v", 
			logPrefix, serverName, serverIP, err)
		return false
	}

	wm := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Code: 0,
		Body: &icmp.Echo{
			ID:   int(id),
			Seq:  seq,
			Data: []byte("ping"),
		},
	}

	wb, err := wm.Marshal(nil)
	if err != nil {
		log.Printf("%sServer %s (%s) - ICMP marshal error: %v", 
			logPrefix, serverName, serverIP, err)
		return false
	}

	var addr net.Addr = &net.IPAddr{IP: ipAddr.IP}
	if _, err := netconn.WriteTo(wb, addr); err != nil {
		log.Printf("%sServer %s (%s) - ICMP write error: %v", 
			logPrefix, serverName, serverIP, err)
		return false
	}

	rb := make([]byte, 1500)
	n, cm, err := netconn.ReadFrom(rb)
	if err != nil {
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			log.Printf("%sServer %s (%s) - Timeout", logPrefix, serverName, serverIP)
			return false
		} else {
			log.Printf("%sServer %s (%s) - ICMP read error: %v", 
				logPrefix, serverName, serverIP, err)
			return false
		}
	}

	rm, err := icmp.ParseMessage(1, rb[:n])
	if err != nil {
		log.Printf("%sServer %s (%s) - ICMP parse error: %v", 
			logPrefix, serverName, serverIP, err)
		return false
	}

	switch rm.Type {
	case ipv4.ICMPTypeEchoReply:
		echoReply, ok := rm.Body.(*icmp.Echo)
		if !ok {
			log.Printf("%sServer %s (%s) - Unexpected body type in ICMP reply", 
				logPrefix, serverName, serverIP)
			return false
		}

		if echoReply.ID == int(id) && echoReply.Seq == seq {
			log.Printf("%sServer %s (%s) - Is alive (ICMP reply ID: %d, Seq: %d)", 
				logPrefix, serverName, serverIP, echoReply.ID, echoReply.Seq)
			return true
		} else {
			log.Printf("%sServer %s (%s) - Invalid ICMP reply: id=%d seq=%d (expected id=%d seq=%d)", 
				logPrefix, serverName, serverIP, echoReply.ID, echoReply.Seq, id, seq)
			return false
		}
	case ipv4.ICMPTypeDestinationUnreachable:
		log.Printf("%sServer %s (%s) - Destination unreachable", 
			logPrefix, serverName, serverIP)
		return false
	default:
		log.Printf("%sServer %s (%s) - Got unexpected ICMP message: %v", 
			logPrefix, serverName, serverIP, rm)
		return false
	}
}

func setServerStatus(server *models.Server, status string, mu *sync.Mutex) {
	mu.Lock()
	defer mu.Unlock()
	server.Status = status
}

func MonitorServers(servers *[]models.Server, interval time.Duration, timeout time.Duration, mu *sync.Mutex, ctx context.Context) {
	var wg sync.WaitGroup

	for i := range *servers {
		if (*servers)[i].IP != "" {
			wg.Add(1)
			go monitorSingleServer(ctx, &(*servers)[i], interval, timeout, mu, &wg)
		}
	}

	wg.Wait()
}

func monitorSingleServer(ctx context.Context, server *models.Server, interval time.Duration, timeout time.Duration, mu *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()

	for {
		startTime := time.Now()

		var pingWg sync.WaitGroup
		pingWg.Add(1)
		PingServer(server, &pingWg, mu, timeout)
		pingWg.Wait()

		elapsed := time.Since(startTime)
		if elapsed < interval {
			sleepTime := interval - elapsed
			time.Sleep(sleepTime)
		}

		select {
		case <-ctx.Done():
			log.Printf("Monitoring for server %s stopped.", server.IP)
			return
		default:
		}
	}
}