package app_test

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudfoundry-incubator/scalable-syslog/adapter/app"
	"github.com/cloudfoundry-incubator/scalable-syslog/api"
	v2 "github.com/cloudfoundry-incubator/scalable-syslog/api/loggregator/v2"
	v1 "github.com/cloudfoundry-incubator/scalable-syslog/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("Adapter", func() {
	var (
		adapterServiceHost string
		adapterHealthAddr  string
		client             v1.AdapterClient
		binding            *v1.Binding
		egressServer       *MockEgressServer
		syslogServer       *syslogTCPServer
	)

	BeforeEach(func() {
		var logsAPIAddr string
		egressServer, logsAPIAddr = startLogsAPIServer()

		rlpTLSConfig, err := api.NewMutualTLSConfig(
			Cert("adapter-rlp.crt"),
			Cert("adapter-rlp.key"),
			Cert("loggregator-ca.crt"),
			"fake-log-provider",
		)
		Expect(err).ToNot(HaveOccurred())

		tlsConfig, err := api.NewMutualTLSConfig(
			Cert("adapter.crt"),
			Cert("adapter.key"),
			Cert("scalable-syslog-ca.crt"),
			"fake-log-provider",
		)
		Expect(err).ToNot(HaveOccurred())

		adapter := app.NewAdapter(
			logsAPIAddr,
			rlpTLSConfig,
			tlsConfig,
			app.WithHealthAddr("localhost:0"),
			app.WithControllerAddr("localhost:0"),
			app.WithLogsEgressAPIConnCount(1),
		)
		adapterHealthAddr, adapterServiceHost = adapter.Start()

		client = startAdapterClient(adapterServiceHost)
		syslogServer = newSyslogTCPServer()

		binding = &v1.Binding{
			AppId:    "app-guid",
			Hostname: "a-hostname",
			Drain:    "syslog://" + syslogServer.Addr().String(),
		}
	})

	It("creates a new binding", func() {
		_, err := client.CreateBinding(context.Background(), &v1.CreateBindingRequest{
			Binding: binding,
		})
		Expect(err).ToNot(HaveOccurred())

		resp, err := client.ListBindings(context.Background(), new(v1.ListBindingsRequest))
		Expect(err).ToNot(HaveOccurred())
		Expect(resp.Bindings).To(HaveLen(1))

		healthResp, err := http.Get(fmt.Sprintf("http://%s/health", adapterHealthAddr))
		Expect(err).ToNot(HaveOccurred())
		Expect(healthResp.StatusCode).To(Equal(http.StatusOK))

		body, err := ioutil.ReadAll(healthResp.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(body).To(MatchJSON(`{"drainCount": 1}`))
	})

	It("deletes a binding", func() {
		_, err := client.CreateBinding(context.Background(), &v1.CreateBindingRequest{
			Binding: binding,
		})
		Expect(err).ToNot(HaveOccurred())

		_, err = client.DeleteBinding(context.Background(), &v1.DeleteBindingRequest{
			Binding: binding,
		})
		Expect(err).ToNot(HaveOccurred())

		healthResp, err := http.Get(fmt.Sprintf("http://%s/health", adapterHealthAddr))
		Expect(err).ToNot(HaveOccurred())
		Expect(healthResp.StatusCode).To(Equal(http.StatusOK))

		body, err := ioutil.ReadAll(healthResp.Body)
		Expect(err).ToNot(HaveOccurred())
		Expect(body).To(MatchJSON(`{"drainCount": 0}`))
	})

	It("connects the logs egress API", func() {
		_, err := client.CreateBinding(context.Background(), &v1.CreateBindingRequest{
			Binding: binding,
		})
		Expect(err).ToNot(HaveOccurred())

		Eventually(egressServer.receiver).Should(HaveLen(1))
	})

	It("forwards logs from loggregator to a syslog drain", func() {
		By("creating a binding", func() {
			_, err := client.CreateBinding(context.Background(), &v1.CreateBindingRequest{
				Binding: binding,
			})
			Expect(err).ToNot(HaveOccurred())

			Eventually(func() int { return syslogServer.msgCount }).Should(BeNumerically(">", 10))
		})

		By("deleting a binding", func() {
			_, err := client.DeleteBinding(context.Background(), &v1.DeleteBindingRequest{
				Binding: binding,
			})
			Expect(err).ToNot(HaveOccurred())

			currentCount := syslogServer.msgCount
			Consistently(func() int { return syslogServer.msgCount }, "100ms").Should(BeNumerically("~", currentCount, 2))
		})
	})
})

func startAdapterClient(addr string) v1.AdapterClient {
	tlsConfig, err := api.NewMutualTLSConfig(
		Cert("adapter.crt"),
		Cert("adapter.key"),
		Cert("scalable-syslog-ca.crt"),
		"adapter",
	)
	Expect(err).ToNot(HaveOccurred())

	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	Expect(err).ToNot(HaveOccurred())

	return v1.NewAdapterClient(conn)
}

func startLogsAPIServer() (*MockEgressServer, string) {
	lis, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	tlsConfig, err := api.NewMutualTLSConfig(
		Cert("fake-log-provider.crt"),
		Cert("fake-log-provider.key"),
		Cert("loggregator-ca.crt"),
		"fake-log-provider",
	)
	Expect(err).ToNot(HaveOccurred())

	mockEgressServer := NewMockEgressServer()
	grpcServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsConfig)))
	v2.RegisterEgressServer(grpcServer, mockEgressServer)

	go func() {
		log.Fatalf("failed to serve: %v", grpcServer.Serve(lis))
	}()

	return mockEgressServer, lis.Addr().String()
}

type MockEgressServer struct {
	receiver chan v2.Egress_ReceiverServer
}

func NewMockEgressServer() *MockEgressServer {
	return &MockEgressServer{
		receiver: make(chan v2.Egress_ReceiverServer, 5),
	}
}

func (m *MockEgressServer) Receiver(req *v2.EgressRequest, receiver v2.Egress_ReceiverServer) error {
	m.receiver <- receiver

	for {
		Expect(receiver.Send(buildLogEnvelope())).To(Succeed())
		time.Sleep(time.Millisecond)
	}

	return nil
}

type syslogTCPServer struct {
	lis       net.Listener
	connCount uint64
	mu        sync.Mutex
	data      []byte
	msgCount  int
}

func newSyslogTCPServer() *syslogTCPServer {
	lis, err := net.Listen("tcp", ":0")
	Expect(err).ToNot(HaveOccurred())
	m := &syslogTCPServer{
		lis: lis,
	}
	go m.accept()
	return m
}

func (m *syslogTCPServer) accept() {
	for {
		conn, err := m.lis.Accept()
		if err != nil {
			return
		}
		atomic.AddUint64(&m.connCount, 1)
		go m.handleConn(conn)
	}
}

func (m *syslogTCPServer) handleConn(conn net.Conn) {
	for {
		buf := make([]byte, 1024)
		_, err := conn.Read(buf)
		if err != nil {
			return
		}
		m.mu.Lock()
		_ = buf
		m.msgCount++
		m.mu.Unlock()
	}
}

func (m *syslogTCPServer) ConnCount() uint64 {
	return atomic.LoadUint64(&m.connCount)
}

func (m *syslogTCPServer) RXData() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return string(m.data)
}

func (m *syslogTCPServer) Addr() net.Addr {
	return m.lis.Addr()
}

func buildLogEnvelope() *v2.Envelope {
	return &v2.Envelope{
		Tags: map[string]*v2.Value{
			"source_type":     {&v2.Value_Text{"APP"}},
			"source_instance": {&v2.Value_Text{"2"}},
		},
		Timestamp: 12345678,
		SourceId:  "app-guid",
		Message: &v2.Envelope_Log{
			Log: &v2.Log{
				Payload: []byte("log"),
				Type:    v2.Log_OUT,
			},
		},
	}
}
