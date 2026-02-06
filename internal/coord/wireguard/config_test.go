package wireguard

import (
	"strings"
	"testing"
)

func TestGenerateClientConfig(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
		ClientMeshIP:     "172.30.100.1",
		ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
		ServerEndpoint:   "wg.example.com:51820",
		DNSServer:        "172.30.0.1",
		MeshCIDR:         "172.30.0.0/16",
		MTU:              1420,
	}

	config := GenerateClientConfig(params)

	// Check [Interface] section
	if !strings.Contains(config, "[Interface]") {
		t.Error("config should contain [Interface] section")
	}
	if !strings.Contains(config, "PrivateKey = "+params.ClientPrivateKey) {
		t.Error("config should contain client private key")
	}
	if !strings.Contains(config, "Address = "+params.ClientMeshIP+"/32") {
		t.Error("config should contain client address")
	}
	if !strings.Contains(config, "DNS = "+params.DNSServer) {
		t.Error("config should contain DNS server")
	}
	if !strings.Contains(config, "MTU = 1420") {
		t.Error("config should contain MTU")
	}

	// Check [Peer] section
	if !strings.Contains(config, "[Peer]") {
		t.Error("config should contain [Peer] section")
	}
	if !strings.Contains(config, "PublicKey = "+params.ServerPublicKey) {
		t.Error("config should contain server public key")
	}
	if !strings.Contains(config, "Endpoint = "+params.ServerEndpoint) {
		t.Error("config should contain server endpoint")
	}
	if !strings.Contains(config, "AllowedIPs = "+params.MeshCIDR) {
		t.Error("config should contain allowed IPs (mesh CIDR)")
	}
	if !strings.Contains(config, "PersistentKeepalive = 25") {
		t.Error("config should contain persistent keepalive")
	}
}

func TestGenerateClientConfigNoDNS(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
		ClientMeshIP:     "172.30.100.1",
		ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
		ServerEndpoint:   "wg.example.com:51820",
		DNSServer:        "", // No DNS
		MeshCIDR:         "172.30.0.0/16",
		MTU:              1420,
	}

	config := GenerateClientConfig(params)

	// Should not contain DNS line when DNSServer is empty
	if strings.Contains(config, "DNS =") {
		t.Error("config should not contain DNS when DNSServer is empty")
	}
}

func TestGenerateClientConfigFormat(t *testing.T) {
	params := ClientConfigParams{
		ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
		ClientMeshIP:     "172.30.100.1",
		ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
		ServerEndpoint:   "wg.example.com:51820",
		MeshCIDR:         "172.30.0.0/16",
		MTU:              1420,
	}

	config := GenerateClientConfig(params)

	// Config should be valid INI format
	lines := strings.Split(config, "\n")

	// First non-empty line should be [Interface]
	foundInterface := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == "[Interface]" {
			foundInterface = true
			break
		}
	}
	if !foundInterface {
		t.Error("config should start with [Interface] section")
	}

	// Should have [Peer] section after [Interface]
	interfaceIndex := -1
	peerIndex := -1
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "[Interface]" {
			interfaceIndex = i
		}
		if line == "[Peer]" {
			peerIndex = i
		}
	}
	if peerIndex <= interfaceIndex {
		t.Error("[Peer] section should come after [Interface] section")
	}
}

func TestClientConfigParamsValidation(t *testing.T) {
	tests := []struct {
		name    string
		params  ClientConfigParams
		wantErr bool
	}{
		{
			name: "valid params",
			params: ClientConfigParams{
				ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
				ClientMeshIP:     "172.30.100.1",
				ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
				ServerEndpoint:   "wg.example.com:51820",
				MeshCIDR:         "172.30.0.0/16",
			},
			wantErr: false,
		},
		{
			name: "missing private key",
			params: ClientConfigParams{
				ClientMeshIP:    "172.30.100.1",
				ServerPublicKey: "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
				ServerEndpoint:  "wg.example.com:51820",
				MeshCIDR:        "172.30.0.0/16",
			},
			wantErr: true,
		},
		{
			name: "missing mesh IP",
			params: ClientConfigParams{
				ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
				ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
				ServerEndpoint:   "wg.example.com:51820",
				MeshCIDR:         "172.30.0.0/16",
			},
			wantErr: true,
		},
		{
			name: "missing server public key",
			params: ClientConfigParams{
				ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
				ClientMeshIP:     "172.30.100.1",
				ServerEndpoint:   "wg.example.com:51820",
				MeshCIDR:         "172.30.0.0/16",
			},
			wantErr: true,
		},
		{
			name: "missing endpoint",
			params: ClientConfigParams{
				ClientPrivateKey: "yAnz5TF+lXXJte14tji3zlMNq+hd2rYUIgJBgB3fBmk=",
				ClientMeshIP:     "172.30.100.1",
				ServerPublicKey:  "HIgo9xNzJMWLKASShiTqIybxZ0U3wGLiUeJ1PKf8ykI=",
				MeshCIDR:         "172.30.0.0/16",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.params.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
