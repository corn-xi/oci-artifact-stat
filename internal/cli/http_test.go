package cli

import (
	"testing"
	"time"
)

func TestApplyHTTP(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    httpPreset
		wantErr bool
	}{
		{
			name: "preset",
			spec: "patient",
			want: httpPreset{10 * time.Second, 60 * time.Second, 3, 5 * time.Second},
		},
		{
			name: "individual overrides leave the rest at their defaults",
			spec: "timeout=30s,retries=4",
			want: httpPreset{defaultConnectTimeout, 30 * time.Second, 4, defaultRetryDelay},
		},
		{
			// The reason later entries win: "the patient preset, but five
			// retries" is a thing people actually want to say.
			name: "a preset can be amended",
			spec: "patient,retries=5",
			want: httpPreset{10 * time.Second, 60 * time.Second, 5, 5 * time.Second},
		},
		{
			name: "bare seconds are accepted, as the replaced flags did",
			spec: "timeout=45",
			want: httpPreset{defaultConnectTimeout, 45 * time.Second, defaultRetries, defaultRetryDelay},
		},
		{name: "unknown preset", spec: "zippy", wantErr: true},
		{name: "unknown key", spec: "timout=5s", wantErr: true},
		{name: "negative retries", spec: "retries=-1", wantErr: true},
		{name: "unparseable duration", spec: "timeout=soon", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := defaultConfig()
			err := cfg.applyHTTP(tt.spec)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("applyHTTP(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			got := httpPreset{cfg.connectTimeout, cfg.maxTime, cfg.retries, cfg.retryDelay}
			if got != tt.want {
				t.Errorf("applyHTTP(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

// The deprecated CURL_* variables keep tuning the same settings.
func TestCurlEnvStillTunesTheSameSettings(t *testing.T) {
	t.Setenv("CURL_MAX_TIME", "42")
	t.Setenv("CURL_RETRY", "7")
	cfg := defaultConfig()
	cfg.applyEnv(func(string, ...any) {})

	if cfg.maxTime != 42*time.Second || cfg.retries != 7 {
		t.Errorf("maxTime = %v, retries = %d; want 42s and 7", cfg.maxTime, cfg.retries)
	}
}
