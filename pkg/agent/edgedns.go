package agent

import (
	"flag"
	"os"
	"path"
	"text/template"

	"github.com/coredns/caddy"
	_ "github.com/coredns/coredns/plugin/bind"
	_ "github.com/coredns/coredns/plugin/cache"
	_ "github.com/coredns/coredns/plugin/debug"
	_ "github.com/coredns/coredns/plugin/dns64"
	_ "github.com/coredns/coredns/plugin/errors"
	_ "github.com/coredns/coredns/plugin/forward"
	_ "github.com/coredns/coredns/plugin/health"
	_ "github.com/coredns/coredns/plugin/hosts"
	_ "github.com/coredns/coredns/plugin/loadbalance"
	_ "github.com/coredns/coredns/plugin/log"
	_ "github.com/coredns/coredns/plugin/loop"
	_ "github.com/coredns/coredns/plugin/metrics"
	_ "github.com/coredns/coredns/plugin/pprof"
	_ "github.com/coredns/coredns/plugin/ready"
	_ "github.com/coredns/coredns/plugin/reload"
	_ "github.com/coredns/coredns/plugin/rewrite"
	_ "github.com/coredns/coredns/plugin/template"
	_ "github.com/coredns/coredns/plugin/trace"
	_ "github.com/coredns/coredns/plugin/whoami"
	// use custom kubernetes plugin to cope with kubeedge issue
	// https://github.com/kubeedge/kubeedge/issues/4582
	_ "github.com/fabedge/fabedge/coredns/plugin/kubernetes"
)

const (
	corefileTemplate = `
.:53 {
    errors
    {{ if .Debug -}}
    log
    debug
    {{ end -}}
    kubernetes {{ .ClusterDomain }} in-addr.arpa ip6.arpa {
      endpoint http://127.0.0.1:10550
      pods insecure
      fallthrough in-addr.arpa ip6.arpa
    }
    forward . /etc/resolv.conf {
      prefer_udp
    }
	{{ if .Probe -}}
    health {
        lameduck 5s
    }
    ready
    {{ end -}}
    cache 30
    loop
    reload
    bind {{ .BindIP }}
}
`
)

func (m *Manager) runCoreDNS() {
	tpl, err := template.New("corefile").Parse(corefileTemplate)
	if err != nil {
		m.log.Error(err, "failed to create corefile template")
		return
	}

	corefilePath := path.Join(m.Workdir, "Corefile")
	file, err := os.OpenFile(corefilePath, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		m.log.Error(err, "failed to open corefile")
		return
	}
	defer file.Close()

	if err = tpl.Execute(file, m.DNS); err != nil {
		m.log.Error(err, "failed to write config to Corefile")
		return
	}

	if err = flag.Set("conf", corefilePath); err != nil {
		m.log.Error(err, "failed to write config to Corefile")
		return
	}

	// Get Corefile input
	corefile, err := caddy.LoadCaddyfile("dns")
	if err != nil {
		m.log.Error(err, "failed to load caddy file")
		return
	}

	instance, err := caddy.Start(corefile)
	if err != nil {
		m.log.Error(err, "failed to start caddy")
		return
	}

	instance.Wait()
}
