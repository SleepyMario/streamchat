package chat

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRoleSetJSONRoundTripUsesProviderNeutralNames(t *testing.T) {
	var roles RoleSet
	roles.Add(RoleSubscriber)
	roles.Add(RoleBroadcaster)
	roles.Add(RoleVIP)
	message := Message{Roles: roles}
	encoded, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"roles":["broadcaster","vip","subscriber"]`) {
		t.Fatalf("roles JSON=%s", encoded)
	}
	var decoded Message
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleBroadcaster, RoleVIP, RoleSubscriber} {
		if !decoded.Roles.Has(role) {
			t.Fatalf("decoded roles %v missing %v", decoded.Roles, role)
		}
	}
}

func TestRoleSetReadsLegacyNamesAndOmitsUnknownRoles(t *testing.T) {
	var message Message
	if err := json.Unmarshal([]byte(`{"roles":["owner","moderator","member","verified","unknown"]}`), &message); err != nil {
		t.Fatal(err)
	}
	for _, role := range []Role{RoleBroadcaster, RoleModerator, RoleSubscriber} {
		if !message.Roles.Has(role) {
			t.Fatalf("legacy roles %v missing %v", message.Roles, role)
		}
	}
	for _, role := range []Role{RolePartner, RoleVIP, RoleOG, RoleFollower} {
		if message.Roles.Has(role) {
			t.Fatalf("unknown legacy role unexpectedly mapped to %v", role)
		}
	}
	empty, err := json.Marshal(Message{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), `"roles"`) {
		t.Fatalf("empty roles were not omitted: %s", empty)
	}
}
