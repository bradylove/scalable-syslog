package app

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"strings"

	"github.com/cloudfoundry-incubator/scalable-syslog/scheduler/internal/ingress"
)

type Config struct {
	HealthHostport    string
	PprofHostport     string
	APIURL            string
	APICAFile         string
	APICertFile       string
	APIKeyFile        string
	APICommonName     string
	APISkipCertVerify bool
	CAFile            string
	CertFile          string
	KeyFile           string
	AdapterCommonName string
	AdapterIPs        string
	AdapterPort       string
	AdapterAddrs      []string
	Blacklist         *ingress.IPRanges
}

func LoadConfig(args []string) (*Config, error) {
	var cfg Config

	flags := flag.NewFlagSet("config", flag.ContinueOnError)

	flags.StringVar(&cfg.HealthHostport, "health", ":8080", "The hostport to listen for health requests")
	flags.StringVar(&cfg.PprofHostport, "pprof", ":6060", "The hostport to listen for pprof")

	flags.StringVar(&cfg.APIURL, "api-url", "", "The URL of the binding provider")
	flags.StringVar(&cfg.APICAFile, "api-ca", "", "The file path for the CA cert")
	flags.StringVar(&cfg.APICertFile, "api-cert", "", "The file path for the client cert")
	flags.StringVar(&cfg.APIKeyFile, "api-key", "", "The file path for the client key")
	flags.StringVar(&cfg.APICommonName, "api-cn", "", "The common name used for the TLS config")
	flags.BoolVar(&cfg.APISkipCertVerify, "api-skip-cert-verify", false, "The option to allow insecure SSL connections")

	flags.StringVar(&cfg.CAFile, "ca", "", "The file path for the CA cert")
	flags.StringVar(&cfg.CertFile, "cert", "", "The file path for the adapter server cert")
	flags.StringVar(&cfg.KeyFile, "key", "", "The file path for the adapter server key")

	flags.StringVar(&cfg.AdapterCommonName, "adapter-cn", "", "The common name used for the TLS config")
	flags.StringVar(&cfg.AdapterPort, "adapter-port", "", "The port of the adapter API")
	flags.StringVar(&cfg.AdapterIPs, "adapter-ips", "", "Comma separated list of adapter IP addresses")

	var blacklist string
	flags.StringVar(&blacklist, "blacklist-ranges", "", "Comma separated list of blacklist IP ranges")

	flags.Parse(args)

	var errs []error
	flags.VisitAll(func(f *flag.Flag) {
		if f.Name != "blacklist-ranges" && f.Value.String() == "" {
			errs = append(errs, fmt.Errorf("Missing required flag %s", f.Name))
		}
	})

	if len(errs) != 0 {
		var errorMsg string
		for _, e := range errs {
			errorMsg += fmt.Sprintf("  %s\n", e.Error())
		}

		return nil, fmt.Errorf("Config validation failed:\n%s", errorMsg)
	}

	var err error
	cfg.AdapterAddrs, err = parseAddrs(cfg.AdapterIPs, cfg.AdapterPort)
	if err != nil {
		return nil, fmt.Errorf("No adapter addresses: %s", err)
	}

	cfg.Blacklist, err = parseBlacklist(blacklist)
	if err != nil {
		return nil, err
	}

	return &cfg, nil
}

func parseBlacklist(blacklist string) (*ingress.IPRanges, error) {
	ipRanges := strings.Split(blacklist, ",")
	blacklistRanges := make([]ingress.IPRange, 0)

	if len(ipRanges) == 1 && len(ipRanges[0]) == 0 {
		ipRanges = []string{}
	}

	for _, ipRange := range ipRanges {
		ips := strings.Split(ipRange, "-")
		r := ingress.IPRange{
			Start: ips[0],
			End:   ips[1],
		}
		blacklistRanges = append(blacklistRanges, r)
	}

	result, err := ingress.NewIPRanges(blacklistRanges...)
	if err != nil {
		return nil, fmt.Errorf("Failed to parse blacklist ip ranges: %s", err)
	}
	return result, nil

}

func parseAddrs(ips, port string) ([]string, error) {
	var hostports []string

	if len(ips) == 0 {
		return nil, errors.New("no IP addresses provided")
	}

	hosts := strings.Split(ips, ",")

	for _, h := range hosts {
		if net.ParseIP(h) == nil {
			return nil, fmt.Errorf("invalid IP format: %s", h)
		}
		hp := fmt.Sprintf("%s:%s", h, port)
		hostports = append(hostports, hp)
	}

	return hostports, nil
}