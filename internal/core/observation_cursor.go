package core

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

var errObservationPageReset = errors.New("page reset required")

// A page is a continuation of one observed revision, not a best-effort offset.
// Any intervening observable write requires an explicit restart from "0".
type observationKey struct {
	Version    int    `json:"v"`
	Schema     int    `json:"schema"`
	Projection string `json:"projection"`
	Store      string `json:"store"`
	Kind       string `json:"kind"`
	Project    string `json:"project"`
	Revision   int64  `json:"revision"`
	Created    string `json:"created"`
	ID         string `json:"id"`
}

func (k observationKey) encode() string {
	b, _ := json.Marshal(k)
	return "k1." + base64.RawURLEncoding.EncodeToString(b)
}
func observationPageKey(ctx context.Context, s *store, q sqlQueryer, sel ObservationSelector) (observationKey, error) {
	k := observationKey{Version: 1, Projection: "created-id-v1", Kind: sel.Kind, Project: sel.ProjectID}
	var err error
	k.Store, err = notificationQueueIdentity(s)
	if err != nil {
		return k, err
	}
	if err = q.QueryRowContext(ctx, "PRAGMA user_version").Scan(&k.Schema); err != nil {
		return k, err
	}
	if err = q.QueryRowContext(ctx, "SELECT revision FROM observation_revision WHERE singleton=1").Scan(&k.Revision); err != nil {
		return k, err
	}
	if sel.Cursor == "" || sel.Cursor == "0" {
		return k, nil
	}
	if len(sel.Cursor) < 3 || sel.Cursor[:3] != "k1." {
		return k, fmt.Errorf("%w: keyset cursor required; restart from 0", errObservationPageReset)
	}
	b, err := base64.RawURLEncoding.DecodeString(sel.Cursor[3:])
	if err != nil {
		return k, err
	}
	var old observationKey
	if _, err = decodeStrictJSONObject(b, 2048, &old); err != nil {
		return k, err
	}
	probe := old
	probe.Created = ""
	probe.ID = ""
	if probe != k {
		return k, fmt.Errorf("%w: revision or selector/store/schema changed; restart from 0", errObservationPageReset)
	}
	return old, nil
}
