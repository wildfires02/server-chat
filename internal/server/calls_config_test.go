package server

import "testing"

func TestValidateICEServersRequiresCredentialsForTURN(t *testing.T) {
	hasTURN, err := validateICEServers([]iceServer{{Urls: []string{"stun:stun.example.test"}}})
	if err != nil || hasTURN {
		t.Fatalf("STUN validation = turn:%t err:%v", hasTURN, err)
	}
	if _, err = validateICEServers([]iceServer{{Urls: []string{"turn:turn.example.test:3478"}}}); err == nil {
		t.Fatal("TURN without credentials was accepted")
	}
	hasTURN, err = validateICEServers([]iceServer{{
		Username: "user", Credential: "secret",
		Urls: []string{"turn:turn.example.test:3478", "turns:turn.example.test:5349"},
	}})
	if err != nil || !hasTURN {
		t.Fatalf("valid TURN configuration = turn:%t err:%v", hasTURN, err)
	}
}
