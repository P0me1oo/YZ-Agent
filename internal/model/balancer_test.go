package model

import (
	"testing"

	"github.com/P0me1oo/YZ-Agent/internal/balancer"
	"github.com/P0me1oo/YZ-Agent/internal/config"
)

func TestBalancerReceiverValidation(t *testing.T) {
	for _, network := range []string{"", "tcp", "raw"} {
		node := &NodeSpec{Protocol: "vless", TLS: 2, Network: network, KernelType: "xray", BalancerReceiver: &balancer.ReceiverConfig{TrustedAddresses: []string{"192.0.2.1", "2001:db8::1"}}}
		if err := ValidateNodeSpec(node, config.KernelConfig{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{"", "0.0.0.0", "::", "0.0.0.0/0", "example.com"} {
		node := &NodeSpec{Protocol: "vless", TLS: 2, KernelType: "xray", BalancerReceiver: &balancer.ReceiverConfig{TrustedAddresses: []string{value}}}
		if err := ValidateNodeSpec(node, config.KernelConfig{}); err == nil {
			t.Fatalf("接受了无效可信来源 %q", value)
		}
	}
	node := &NodeSpec{Protocol: "vless", TLS: 2, Network: "grpc", KernelType: "xray", BalancerReceiver: &balancer.ReceiverConfig{TrustedAddresses: []string{"192.0.2.1"}}}
	if err := ValidateNodeSpec(node, config.KernelConfig{}); err == nil {
		t.Fatal("前置池接受了非 TCP/RAW 入口")
	}
}
