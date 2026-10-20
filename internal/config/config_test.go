package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFromEnvDefaults(t *testing.T) {
	cfg, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != DefaultListenAddr || cfg.DataDir != DefaultDataDir || cfg.SessionSecure {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	cfg, err := FromEnv(env(map[string]string{
		EnvListenAddr:    "0.0.0.0:9000",
		EnvDataDir:       "/data",
		EnvSessionSecure: "true",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddr != "0.0.0.0:9000" || cfg.DataDir != "/data" || !cfg.SessionSecure {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestFromEnvBounds(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"no port":         {EnvListenAddr: "127.0.0.1"},
		"port zero":       {EnvListenAddr: "127.0.0.1:0"},
		"port too large":  {EnvListenAddr: "127.0.0.1:65536"},
		"port not number": {EnvListenAddr: "127.0.0.1:http"},
		"bad bool":        {EnvSessionSecure: "maybe"},
	} {
		if _, err := FromEnv(env(vars)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	for name, vars := range map[string]map[string]string{
		"max port":    {EnvListenAddr: "[::1]:65535"},
		"min port":    {EnvListenAddr: ":1"},
		"blank value": {EnvDataDir: "   "},
	} {
		if _, err := FromEnv(env(vars)); err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
		}
	}
}

func TestValidateRejectsEmptyDataDir(t *testing.T) {
	if err := (Config{ListenAddr: "127.0.0.1:8080"}).Validate(); err == nil {
		t.Fatal("expected an error for an empty data directory")
	}
}
