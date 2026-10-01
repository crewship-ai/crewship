package docker

import (
	"errors"
	"reflect"
	"testing"
)

// Teardown releases and removes the helper image first and the Docker volume
// last. The Docker volume is the only durable record that a quota image
// exists for the crew, so it must outlive every helper step: a failure in
// the middle leaves it in place and the next teardown retries.
func TestQuotaVolumeTeardownOrder(t *testing.T) {
	cases := []struct {
		name           string
		releaseErr     error
		removeErr      error
		dockerErr      error
		wantTrace      []string
		wantDockerGone bool
		wantErr        bool
	}{
		{name: "clean teardown", wantTrace: []string{"catalog-release", "catalog-remove", "docker-volume-remove"}, wantDockerGone: true},
		{name: "helper remove fails", removeErr: errors.New("alias still mounted"), wantTrace: []string{"catalog-release", "catalog-remove"}, wantErr: true},
		{name: "helper release fails", releaseErr: errors.New("helper down"), wantTrace: []string{"catalog-release"}, wantErr: true},
		{name: "docker remove fails after helper", dockerErr: errors.New("daemon busy"), wantTrace: []string{"catalog-release", "catalog-remove", "docker-volume-remove"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var trace []string
			daemon := newFakeQuotaDaemon(t)
			daemon.trace = &trace
			daemon.volumeRemoveErr = tc.dockerErr
			daemon.volumes["crewship-quota-syntheticquota"] = existingQuotaVolume("alpha", nil)
			catalog := &fakeQuotaCatalog{trace: &trace, removeErr: tc.removeErr, releaseErr: tc.releaseErr}
			p := newCovProvider(t, Config{QuotaCatalog: catalog}, daemon.ServeHTTP)
			err := p.RemoveCrewServiceVolumes(t.Context(), covCrewID, "alpha")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(trace, tc.wantTrace) {
				t.Fatalf("teardown order %v, want %v", trace, tc.wantTrace)
			}
			if gone := len(daemon.volumeRemoves) == 1; gone != tc.wantDockerGone {
				t.Fatalf("docker volume removed=%v, want %v", gone, tc.wantDockerGone)
			}
		})
	}
}
