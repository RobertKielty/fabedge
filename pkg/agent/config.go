// Copyright 2021 BoCloud
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"fmt"
	"net"
	"time"

	debpkg "github.com/bep/debounce"
	"github.com/coreos/go-iptables/iptables"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/exec"

	"github.com/fabedge/fabedge/pkg/tunnel/strongswan"
	"github.com/fabedge/fabedge/pkg/util/ipset"
	"github.com/fabedge/fabedge/third_party/ipvs"
)

type Config struct {
	LocalCerts       []string
	SyncPeriod       time.Duration
	DebounceDuration time.Duration
	TunnelsConfPath  string
	ServicesConfPath string
	MASQOutgoing     bool

	DummyInterfaceName string

	EnableHairpinMode bool
	NetworkPluginMTU  int
	CNI               struct {
		Version     string
		ConfDir     string
		NetworkName string
		BridgeName  string
	}

	DNS struct {
		Enabled       bool
		BindIP        string
		ClusterDomain string
		Debug         bool
		Probe         bool
	}

	EnableProxy bool

	EnableAutoNetworking bool
	MulticastAddress     string
	MulticastToken       string
	MulticastInterval    time.Duration
	EndpointTTL          time.Duration
	BackupInterval       time.Duration
	Workdir              string
}

func (cfg *Config) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&cfg.TunnelsConfPath, "tunnels-conf", "/etc/fabedge/tunnels.yaml", "The path to tunnels configuration file")
	fs.StringVar(&cfg.ServicesConfPath, "services-conf", "/etc/fabedge/services.yaml", "The file that records information about services and endpointslices")

	fs.StringSliceVar(&cfg.LocalCerts, "local-cert", []string{"edgecert.pem"}, "The path to cert files, comma separated. If it's a relative path, the cert file should be put under /etc/ipsec.d/certs")
	fs.DurationVar(&cfg.DebounceDuration, "debounce", time.Second, "The debounce delay to avoid too much network reconfiguring")
	fs.DurationVar(&cfg.SyncPeriod, "sync-period", 30*time.Second, "The period to synchronize network configuration")

	fs.BoolVar(&cfg.EnableHairpinMode, "enable-hairpinmode", true, "enable the Hairpin feature")
	fs.IntVar(&cfg.NetworkPluginMTU, "network-plugin-mtu", 1400, "Set network plugin MTU for edge nodes")
	fs.StringVar(&cfg.CNI.Version, "cni-version", "0.3.1", "cni version")
	fs.StringVar(&cfg.CNI.ConfDir, "cni-conf-path", "/etc/cni/net.d", "cni version")
	fs.StringVar(&cfg.CNI.NetworkName, "cni-network-name", "fabedge", "the name of network")
	fs.StringVar(&cfg.CNI.BridgeName, "cni-bridge-name", "br-fabedge", "the name of bridge")

	fs.BoolVar(&cfg.MASQOutgoing, "masq-outgoing", true, "Configure faberge networking to perform outbound NAT for connections from pods to outside of the cluster")
	fs.BoolVar(&cfg.EnableProxy, "enable-proxy", true, "Enable the proxy feature")

	fs.StringVar(&cfg.DummyInterfaceName, "dummy-interface-name", "fabedge-ipvs0", "The name of dummy interface")
	fs.BoolVar(&cfg.DNS.Enabled, "enable-dns", false, "Enable DNS component")
	fs.BoolVar(&cfg.DNS.Debug, "dns-debug", false, "Enable debug plugin of DNS component")
	fs.BoolVar(&cfg.DNS.Probe, "dns-probe", false, "Enable ready and health plugins of DNS component")
	fs.StringVar(&cfg.DNS.BindIP, "dns-bind-ip", "169.254.25.10", "The IP for DNS component to bind")
	fs.StringVar(&cfg.DNS.ClusterDomain, "dns-cluster-domain", "cluster.local", "The kubernetes cluster's domain name")

	fs.BoolVar(&cfg.EnableAutoNetworking, "auto-networking", false, "Enable auto-networking which will find endpoints in the same LAN")
	fs.StringVar(&cfg.Workdir, "workdir", "/var/lib/fabedge", "The working directory for fabedge")
	fs.StringVar(&cfg.MulticastAddress, "multicast-address", "239.40.20.81:18080", "The multicast address to exchange endpoints")
	fs.StringVar(&cfg.MulticastToken, "multicast-token", "", "Token used for multicasting endpoint")
	fs.DurationVar(&cfg.MulticastInterval, "multicast-interval", 5*time.Second, "The interval between endpoint multicasting")
	fs.DurationVar(&cfg.BackupInterval, "backup-interval", 10*time.Second, "The interval between local endpoints backing up")
	fs.DurationVar(&cfg.EndpointTTL, "endpoint-ttl", 20*time.Second, "The time to live for endpoint received from multicasting")
}

func (cfg *Config) Validate() error {
	if cfg.DebounceDuration < time.Second {
		return fmt.Errorf("the least debounce value is 1 second")
	}

	if cfg.SyncPeriod < time.Second {
		return fmt.Errorf("the least sync period value is 1 second")
	}

	if cfg.EnableAutoNetworking {
		_, err := net.ResolveUDPAddr("udp", cfg.MulticastAddress)
		if err != nil {
			return err
		}

		if cfg.MulticastToken == "" {
			return fmt.Errorf("broadcast token is required")
		}

		if cfg.EndpointTTL < cfg.MulticastInterval {
			cfg.EndpointTTL = 2 * cfg.MulticastInterval
		}
	}

	if cfg.DNS.Enabled {
		if net.ParseIP(cfg.DNS.BindIP) == nil {
			return fmt.Errorf("invalid DNS bind IP address")
		}
	}

	return nil
}

func (cfg Config) Manager() (*Manager, error) {
	kernelHandler := ipvs.NewLinuxKernelHandler()
	if cfg.EnableProxy {
		if _, err := ipvs.CanUseIPVSProxier(kernelHandler); err != nil {
			return nil, err
		}
	}

	tm, err := strongswan.New(
		strongswan.StartAction("clear"),
		strongswan.DpdDelay("10s"),
	)
	if err != nil {
		return nil, err
	}

	ipt, err := iptables.New()
	if err != nil {
		return nil, err
	}

	ipt6, err := iptables.NewWithProtocol(iptables.ProtocolIPv6)
	if err != nil {
		return nil, err
	}

	m := &Manager{
		Config: cfg,
		tm:     tm,
		ipt4:   ipt,
		ipt6:   ipt6,
		log:    klogr.New().WithName("manager"),

		events:        make(chan struct{}),
		debounce:      debpkg.New(cfg.DebounceDuration),
		peerEndpoints: make(map[string]Endpoint),

		netLink: ipvs.NewNetLinkHandle(false),
		ipvs:    ipvs.New(exec.New()),
		ipset:   ipset.New(),
	}

	return m, nil
}
