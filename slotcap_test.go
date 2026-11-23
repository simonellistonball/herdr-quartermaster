package main

import (
	"strings"
	"testing"
)

func TestCheckSlots(t *testing.T) {
	cfg := Config{CoordinatorName: "quartermaster", Hosts: map[string]HostConfig{"local": {Slots: 2}}}
	one := []Agent{{Name: "quartermaster"}, {Name: "hub-a"}}
	if err := cfg.checkSlots("local", one); err != nil {
		t.Fatalf("1 of 2 (the coordinator does not count): %v", err)
	}
	two := append(one, Agent{Name: "hub-b"})
	err := cfg.checkSlots("local", two)
	if err == nil || !strings.Contains(err.Error(), "2 of 2") {
		t.Fatalf("2 of 2 should refuse, got %v", err)
	}
}
