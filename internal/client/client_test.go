package client

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/sam-jeston/braid/internal/api"
)

func TestCheckCompatible(t *testing.T) {
	cases := []struct {
		name            string
		info            api.ServerInfo
		proto, minSrv   int
		wantErr, update bool
	}{
		{"same protocol", api.ServerInfo{Protocol: 3, MinClientProtocol: 2}, 3, 3, false, false},
		{"older app still supported", api.ServerInfo{Protocol: 4, MinClientProtocol: 2}, 3, 3, false, false},
		{"newer app, server new enough", api.ServerInfo{Protocol: 3, MinClientProtocol: 1}, 5, 3, false, false},
		{"app too old", api.ServerInfo{Protocol: 4, MinClientProtocol: 4}, 3, 3, true, true},
		{"server too old", api.ServerInfo{Protocol: 2, MinClientProtocol: 1}, 4, 3, true, false},
		{"server from before versioning", api.ServerInfo{}, 1, 0, false, false},
		{"server from before versioning, app needs newer", api.ServerInfo{}, 2, 2, true, false},
	}
	for _, c := range cases {
		err := checkCompatible(c.info, c.proto, c.minSrv)
		ie, ok := AsIncompatible(err)
		if (err != nil) != c.wantErr || ok != c.wantErr || (ok && ie.UpdateApp != c.update) {
			t.Errorf("%s: err = %v", c.name, err)
		}
	}
	if err := CheckCompatible(api.ServerInfo{Protocol: api.Protocol, MinClientProtocol: api.MinClientProtocol}); err != nil {
		t.Errorf("a server from this build: %v", err)
	}
}

func TestIncompatibleMessages(t *testing.T) {
	app := (&IncompatibleError{UpdateApp: true, ServerVersion: "v0.4.0"}).Error()
	if !strings.Contains(app, "Update Braid") || !strings.Contains(app, "v0.4.0") {
		t.Errorf("app message: %s", app)
	}
	srv := (&IncompatibleError{ServerVersion: "dev"}).Error()
	if !strings.Contains(srv, "update it") || strings.Contains(srv, "dev") {
		t.Errorf("server message: %s", srv)
	}
}

func TestAsIncompatibleRecognizesServerRejection(t *testing.T) {
	err := fmt.Errorf("refresh: %w", &Error{Status: 426, Code: api.CodeClientOutdated, Message: "update"})
	if ie, ok := AsIncompatible(err); !ok || !ie.UpdateApp {
		t.Errorf("426 from the server: %v %v", ie, ok)
	}
	if _, ok := AsIncompatible(&Error{Status: 401, Code: "unauthorized"}); ok {
		t.Error("an auth error isn't an incompatibility")
	}
	if _, ok := AsIncompatible(errors.New("network down")); ok {
		t.Error("a network error isn't an incompatibility")
	}
}
