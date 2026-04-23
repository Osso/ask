package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestVirtualSessions_RoundTrip(t *testing.T) {
	isolateHome(t)

	now := time.Now().UTC().Truncate(time.Second)
	store := &virtualSessionStore{
		Version: virtualSessionStoreVersion,
		Sessions: []VirtualSession{
			{
				ID:        "vs-1",
				Workspace: "/tmp/ws",
				CreatedAt: now,
				UpdatedAt: now,
				Preview:   "hello",
				LastProvider: "claude",
				ProviderSessions: map[string]ProviderSessionRef{
					"claude": {SessionID: "native-claude", Cwd: "/tmp/ws"},
					"codex":  {SessionID: "native-codex"},
				},
			},
		},
	}
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := loadVirtualSessions()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(got.Sessions))
	}
	vs := got.Sessions[0]
	if vs.ID != "vs-1" || vs.Workspace != "/tmp/ws" || vs.Preview != "hello" {
		t.Errorf("round-trip mismatch: %+v", vs)
	}
	if vs.ProviderSessions["claude"].SessionID != "native-claude" ||
		vs.ProviderSessions["claude"].Cwd != "/tmp/ws" {
		t.Errorf("claude provider ref wrong: %+v", vs.ProviderSessions["claude"])
	}
	if vs.ProviderSessions["codex"].SessionID != "native-codex" {
		t.Errorf("codex provider ref wrong: %+v", vs.ProviderSessions["codex"])
	}
	if vs.LastProvider != "claude" {
		t.Errorf("lastProvider=%q want claude", vs.LastProvider)
	}
}

func TestVirtualSessions_MissingFileIsEmpty(t *testing.T) {
	isolateHome(t)
	got, err := loadVirtualSessions()
	if err != nil {
		t.Fatalf("load on missing: %v", err)
	}
	if got == nil {
		t.Fatal("nil store on missing file")
	}
	if len(got.Sessions) != 0 {
		t.Errorf("want empty sessions, got %d", len(got.Sessions))
	}
}

func TestVirtualSessions_CorruptJSONErrors(t *testing.T) {
	isolateHome(t)
	path, err := virtualSessionsPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	if _, err := loadVirtualSessions(); err == nil {
		t.Error("corrupt JSON should surface an error, got nil")
	}
}

func TestVirtualSessions_FilePerms(t *testing.T) {
	isolateHome(t)
	store := &virtualSessionStore{Version: 1}
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	path, err := virtualSessionsPath()
	if err != nil {
		t.Fatalf("path: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("perms=%o want 0600", mode)
	}
}

func TestUpsertVirtualSession_Creates(t *testing.T) {
	store := &virtualSessionStore{Version: 1}
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	id := upsertVirtualSession(store, "", "/ws", "claude", "native-1", "/ws", "hi there", now)
	if id == "" {
		t.Fatal("upsert returned empty id")
	}
	if len(store.Sessions) != 1 {
		t.Fatalf("want 1 session, got %d", len(store.Sessions))
	}
	vs := store.Sessions[0]
	if vs.ID != id {
		t.Errorf("stored id=%q returned id=%q", vs.ID, id)
	}
	if vs.ProviderSessions["claude"].SessionID != "native-1" {
		t.Errorf("provider mapping wrong: %+v", vs.ProviderSessions)
	}
	if !vs.CreatedAt.Equal(now) || !vs.UpdatedAt.Equal(now) {
		t.Errorf("timestamps wrong: created=%v updated=%v", vs.CreatedAt, vs.UpdatedAt)
	}
	if vs.LastProvider != "claude" {
		t.Errorf("lastProvider=%q want claude", vs.LastProvider)
	}
	if vs.Preview != "hi there" {
		t.Errorf("preview=%q", vs.Preview)
	}
}

func TestUpsertVirtualSession_AddsSecondProvider(t *testing.T) {
	store := &virtualSessionStore{Version: 1}
	t0 := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	id := upsertVirtualSession(store, "", "/ws", "claude", "native-cla", "/ws", "hi", t0)
	// Same VS, second provider lands later.
	t1 := t0.Add(5 * time.Minute)
	got := upsertVirtualSession(store, id, "/ws", "codex", "native-cdx", "/ws", "hi", t1)
	if got != id {
		t.Fatalf("upsert returned %q, want %q", got, id)
	}
	vs := store.Sessions[0]
	if vs.ProviderSessions["claude"].SessionID != "native-cla" {
		t.Errorf("claude mapping lost: %+v", vs.ProviderSessions)
	}
	if vs.ProviderSessions["codex"].SessionID != "native-cdx" {
		t.Errorf("codex mapping missing: %+v", vs.ProviderSessions)
	}
	if !vs.UpdatedAt.Equal(t1) {
		t.Errorf("UpdatedAt not bumped; got %v want %v", vs.UpdatedAt, t1)
	}
	if !vs.CreatedAt.Equal(t0) {
		t.Errorf("CreatedAt changed; got %v want %v", vs.CreatedAt, t0)
	}
	if vs.LastProvider != "codex" {
		t.Errorf("lastProvider=%q want codex after second upsert", vs.LastProvider)
	}
}

func TestUpsertVirtualSession_FindByProviderNativeReattaches(t *testing.T) {
	store := &virtualSessionStore{Version: 1}
	t0 := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	id := upsertVirtualSession(store, "", "/ws", "claude", "native-1", "/ws", "hi", t0)
	// Caller forgot the vsID but passes the same native id; should reattach.
	got := upsertVirtualSession(store, "", "/ws", "claude", "native-1", "/ws", "hi", t0.Add(time.Minute))
	if got != id {
		t.Errorf("expected reattach to %q, got %q (duplicated)", id, got)
	}
	if len(store.Sessions) != 1 {
		t.Errorf("duplicate VS created: %d", len(store.Sessions))
	}
}

func TestFirstUserPreview_FindsFirstUserEntry(t *testing.T) {
	history := []historyEntry{
		{kind: histPrerendered, text: "tool output"},
		{kind: histUser, text: "first user"},
		{kind: histResponse, text: "assistant"},
		{kind: histUser, text: "second user"},
	}
	got := firstUserPreview(history)
	if got != "first user" {
		t.Errorf("preview=%q want 'first user'", got)
	}
}

func TestFirstUserPreview_FlattensNewlines(t *testing.T) {
	got := firstUserPreview([]historyEntry{{kind: histUser, text: "line1\nline2\nline3"}})
	if got != "line1 line2 line3" {
		t.Errorf("preview=%q", got)
	}
}

func TestFirstUserPreview_EmptyWhenNoUserEntries(t *testing.T) {
	got := firstUserPreview([]historyEntry{{kind: histResponse, text: "only assistant"}})
	if got != "" {
		t.Errorf("preview=%q want empty", got)
	}
}

func TestRecordVirtualSession_NewSessionCreatesVS(t *testing.T) {
	isolateHome(t)
	m := newTestModel(t, newFakeProvider())
	m.cwd = "/ws"
	m.history = append(m.history, historyEntry{kind: histUser, text: "hi there"})
	m.recordVirtualSession("native-1")
	if m.virtualSessionID == "" {
		t.Fatal("virtualSessionID should be set after recordVirtualSession")
	}
	store, err := loadVirtualSessions()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(store.Sessions) != 1 {
		t.Fatalf("want 1 VS persisted, got %d", len(store.Sessions))
	}
	vs := store.Sessions[0]
	if vs.ID != m.virtualSessionID {
		t.Errorf("persisted id=%q vs model id=%q", vs.ID, m.virtualSessionID)
	}
	if vs.Workspace != "/ws" {
		t.Errorf("workspace=%q want /ws", vs.Workspace)
	}
	if vs.Preview != "hi there" {
		t.Errorf("preview=%q", vs.Preview)
	}
	ref, ok := vs.ProviderSessions["fake"]
	if !ok || ref.SessionID != "native-1" {
		t.Errorf("provider mapping wrong: %+v", vs.ProviderSessions)
	}
	if ref.Cwd != "/ws" {
		t.Errorf("native cwd=%q want /ws", ref.Cwd)
	}
}

func TestRecordVirtualSession_SameProviderSecondTurnReusesVS(t *testing.T) {
	isolateHome(t)
	m := newTestModel(t, newFakeProvider())
	m.cwd = "/ws"
	m.history = append(m.history, historyEntry{kind: histUser, text: "hi"})
	m.recordVirtualSession("native-1")
	firstID := m.virtualSessionID
	// Second turn same provider, same VS. Native id might update (e.g.
	// claude rewrites session id on compaction), so we pass a fresh one.
	m.recordVirtualSession("native-1-v2")
	if m.virtualSessionID != firstID {
		t.Errorf("VS id changed across turns: %q → %q", firstID, m.virtualSessionID)
	}
	store, _ := loadVirtualSessions()
	if len(store.Sessions) != 1 {
		t.Fatalf("want 1 VS, got %d", len(store.Sessions))
	}
	if got := store.Sessions[0].ProviderSessions["fake"].SessionID; got != "native-1-v2" {
		t.Errorf("native id not updated: got %q want native-1-v2", got)
	}
}

func TestRecordVirtualSession_SecondProviderAddsMapping(t *testing.T) {
	isolateHome(t)
	p1 := newFakeProvider()
	p1.id = "claude"
	m := newTestModel(t, p1)
	m.cwd = "/ws"
	m.history = append(m.history, historyEntry{kind: histUser, text: "hi"})
	m.recordVirtualSession("native-claude")
	vsID := m.virtualSessionID

	// Swap to a different provider but preserve the VS id.
	p2 := newFakeProvider()
	p2.id = "codex"
	m.provider = p2
	m.recordVirtualSession("native-codex")
	if m.virtualSessionID != vsID {
		t.Errorf("VS id changed on provider swap: %q → %q", vsID, m.virtualSessionID)
	}
	store, _ := loadVirtualSessions()
	if len(store.Sessions) != 1 {
		t.Fatalf("want 1 VS, got %d", len(store.Sessions))
	}
	ps := store.Sessions[0].ProviderSessions
	if ps["claude"].SessionID != "native-claude" {
		t.Errorf("claude mapping lost: %+v", ps)
	}
	if ps["codex"].SessionID != "native-codex" {
		t.Errorf("codex mapping missing: %+v", ps)
	}
}

func TestResumeVirtualSession_CurrentProviderMappingUsesNativeID(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider()
	p.id = "claude"
	p.loadHistoryFn = func(id string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{{kind: histUser, text: "loaded-for:" + id}}, nil
	}
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.cwd = "/ws"

	// Seed a VS with a claude mapping.
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "claude", "native-42", "/ws-cwd", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	entry := sessionEntry{id: vsID, virtualSessionID: vsID}
	newM, cmd := m.resumeVirtualSession(entry)
	mm := newM.(model)
	if mm.virtualSessionID != vsID {
		t.Errorf("virtualSessionID=%q want %q", mm.virtualSessionID, vsID)
	}
	if mm.sessionID != "native-42" {
		t.Errorf("sessionID=%q want native-42 (the native id for current provider)", mm.sessionID)
	}
	if mm.resumeCwd != "/ws-cwd" {
		t.Errorf("resumeCwd=%q want /ws-cwd", mm.resumeCwd)
	}
	if cmd == nil {
		t.Fatal("expected loadHistoryCmd, got nil")
	}
	msg := cmd()
	hl, ok := msg.(historyLoadedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want historyLoadedMsg", msg)
	}
	if hl.sessionID != "native-42" {
		t.Errorf("history loaded for sessionID=%q want native-42", hl.sessionID)
	}
	if hl.virtualSessionID != vsID {
		t.Errorf("historyLoadedMsg missing VS id tag: %+v", hl)
	}

	// Run the message through Update to confirm the gate accepts it
	// and the translated history lands on m.history.
	mm2, _ := runUpdate(t, mm, hl)
	if len(mm2.history) == 0 {
		t.Fatal("translated history must render through Update")
	}
	var found bool
	for _, e := range mm2.history {
		if strings.Contains(e.text, "loaded-for:native-42") {
			found = true
		}
	}
	if !found {
		t.Errorf("history missing loaded entries: %+v", mm2.history)
	}
}

func TestResumeVirtualSession_NoMappingForCurrentProviderTranslatesFromSource(t *testing.T) {
	isolateHome(t)
	claude := newFakeProvider()
	claude.id = "claude"
	claude.loadHistoryFn = func(id string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{{kind: histUser, text: "from-claude:" + id}}, nil
	}
	codex := newFakeProvider()
	codex.id = "codex"
	var codexLoaded bool
	codex.loadHistoryFn = func(id string, _ HistoryOpts) ([]historyEntry, error) {
		codexLoaded = true
		return nil, nil
	}
	withRegisteredProviders(t, claude, codex)

	// VS has only a claude mapping; the tab's provider is codex.
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "claude", "c-sess", "/ws", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}
	m := newTestModel(t, codex)
	m.cwd = "/ws"
	entry := sessionEntry{id: vsID, virtualSessionID: vsID}
	newM, cmd := m.resumeVirtualSession(entry)
	mm := newM.(model)
	if mm.virtualSessionID != vsID {
		t.Errorf("virtualSessionID not set: %q", mm.virtualSessionID)
	}
	if mm.sessionID != "" {
		t.Errorf("sessionID=%q must stay empty so next turn spawns fresh codex session", mm.sessionID)
	}
	if mm.resumeCwd != "" {
		t.Errorf("resumeCwd=%q must stay empty", mm.resumeCwd)
	}
	if cmd == nil {
		t.Fatal("expected history load command, got nil")
	}
	msg := cmd()
	hl, ok := msg.(historyLoadedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want historyLoadedMsg", msg)
	}
	// Source should be claude (the provider that has a native id on
	// this VS), not codex — translation reads from the source store.
	if codexLoaded {
		t.Error("codex.LoadHistory must NOT be invoked when codex has no mapping — source is the provider that has the native id")
	}
	if hl.sessionID != "c-sess" {
		t.Errorf("source loaded with id=%q want c-sess", hl.sessionID)
	}
	if len(hl.entries) != 1 || hl.entries[0].text != "from-claude:c-sess" {
		t.Errorf("translated entries wrong: %+v", hl.entries)
	}
	if hl.virtualSessionID != vsID {
		t.Errorf("historyLoadedMsg not tagged with VS id: %+v", hl)
	}

	// Feed through Update: the gate must match on VS id (not on the
	// source's native sessionID) so the translated history actually
	// renders. If the gate dropped the message we'd see an empty
	// m.history and the regression would be invisible to a test that
	// only inspects cmd().
	mm2, _ := runUpdate(t, mm, hl)
	if len(mm2.history) == 0 {
		t.Fatal("translated history must render after Update processes historyLoadedMsg")
	}
	var found bool
	for _, e := range mm2.history {
		if strings.Contains(e.text, "from-claude:c-sess") {
			found = true
		}
	}
	if !found {
		t.Errorf("translated history entries missing; got %+v", mm2.history)
	}
}

func TestApplyProviderSwitch_PreservesVirtualSessionID(t *testing.T) {
	isolateHome(t)
	// Register two distinct providers so a swap means "cross-provider".
	p1 := newFakeProvider()
	p1.id = "fakeA"
	p1.displayName = "Fake A"
	p2 := newFakeProvider()
	p2.id = "fakeB"
	p2.displayName = "Fake B"
	withRegisteredProviders(t, p1, p2)

	m := newTestModel(t, p1)
	m.virtualSessionID = "vs-carry"
	m.sessionID = "native-from-A"
	m.resumeCwd = "/ws"
	m.providerSwitchProvIdx = 1 // target is B

	newM, _ := m.applyProviderSwitch("")
	mm := newM.(model)
	if mm.provider.ID() != "fakeB" {
		t.Fatalf("swap failed: provider=%s", mm.provider.ID())
	}
	// Cross-provider swap drops native id (correct) but the VS id
	// must survive so the next providerDoneMsg's upsert wires the
	// new provider's native id onto the same VS.
	if mm.sessionID != "" {
		t.Errorf("cross-provider swap should clear sessionID, got %q", mm.sessionID)
	}
	if mm.virtualSessionID != "vs-carry" {
		t.Errorf("virtualSessionID dropped on cross-provider swap: got %q want vs-carry", mm.virtualSessionID)
	}
}

func TestResumeVirtualSession_MissingVSInStoreErrorsGracefully(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider()
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	// Empty store, but entry points at a phantom VS.
	entry := sessionEntry{id: "vs-ghost", virtualSessionID: "vs-ghost"}
	newM, cmd := m.resumeVirtualSession(entry)
	mm := newM.(model)
	if mm.mode != modeInput {
		t.Errorf("mode=%v want modeInput after missing VS", mm.mode)
	}
	if cmd != nil {
		t.Errorf("expected nil cmd, got %T", cmd())
	}
	if len(mm.history) == 0 {
		t.Error("expected error message appended to history")
	}
}

func TestResumeVirtualSession_RoundTripUpsertPersistsCodexNativeID(t *testing.T) {
	isolateHome(t)
	claude := newFakeProvider()
	claude.id = "claude"
	claude.loadHistoryFn = func(_ string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{{kind: histUser, text: "hi"}}, nil
	}
	codex := newFakeProvider()
	codex.id = "codex"
	withRegisteredProviders(t, claude, codex)

	// Seed with claude-only mapping.
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "claude", "c-1", "/ws", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Resume into a codex-tabbed model.
	m := newTestModel(t, codex)
	m.cwd = "/ws"
	newM, _ := m.resumeVirtualSession(sessionEntry{id: vsID, virtualSessionID: vsID})
	mm := newM.(model)

	// Simulate the user completing a codex turn: providerDoneMsg with
	// a fresh codex native id. recordVirtualSession must route that id
	// onto the same VS and populate a codex mapping.
	mm.recordVirtualSession("cdx-42")

	got, err := loadVirtualSessions()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Sessions) != 1 {
		t.Fatalf("want 1 VS persisted, got %d: %+v", len(got.Sessions), got.Sessions)
	}
	vs := got.Sessions[0]
	if vs.ID != vsID {
		t.Errorf("VS id=%q want %q (should have reused the existing VS id)", vs.ID, vsID)
	}
	if vs.ProviderSessions["claude"].SessionID != "c-1" {
		t.Errorf("claude mapping lost: %+v", vs.ProviderSessions)
	}
	if vs.ProviderSessions["codex"].SessionID != "cdx-42" {
		t.Errorf("codex mapping not added: %+v", vs.ProviderSessions)
	}
}

func TestListForWorkspace_FiltersAndSorts(t *testing.T) {
	store := &virtualSessionStore{Version: 1}
	t0 := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	upsertVirtualSession(store, "", "/a", "claude", "a1", "/a", "A1", t0)
	upsertVirtualSession(store, "", "/b", "claude", "b1", "/b", "B1", t0.Add(time.Hour))
	upsertVirtualSession(store, "", "/a", "claude", "a2", "/a", "A2", t0.Add(2*time.Hour))

	listA := store.listForWorkspace("/a")
	if len(listA) != 2 {
		t.Fatalf("/a listing got %d, want 2", len(listA))
	}
	// Newest first: A2 before A1.
	if listA[0].Preview != "A2" || listA[1].Preview != "A1" {
		t.Errorf("sort wrong: %+v", listA)
	}
	listB := store.listForWorkspace("/b")
	if len(listB) != 1 || listB[0].Preview != "B1" {
		t.Errorf("/b listing wrong: %+v", listB)
	}
}

// ---- US-007: prelude injection ----

func TestBuildTranslationPrelude_IncludesUserAndAssistantTurns(t *testing.T) {
	entries := []historyEntry{
		{kind: histUser, text: "hi"},
		{kind: histResponse, text: "hello"},
		{kind: histPrerendered, text: "[tool block — should be skipped]"},
		{kind: histUser, text: "more"},
	}
	got := buildTranslationPrelude("Claude", entries)
	if got == "" {
		t.Fatal("expected non-empty prelude")
	}
	if !strings.HasPrefix(strings.TrimLeft(got, " \t\n"), preludeSentinel) {
		t.Errorf("prelude missing sentinel: %q", got)
	}
	if !strings.Contains(got, "Claude") {
		t.Errorf("prelude missing source provider name: %q", got)
	}
	for _, needle := range []string{"## User", "## Assistant", "hi", "hello", "more"} {
		if !strings.Contains(got, needle) {
			t.Errorf("prelude missing %q: %q", needle, got)
		}
	}
	if strings.Contains(got, "[tool block") {
		t.Errorf("prerendered tool block should not leak into prelude: %q", got)
	}
	if !strings.Contains(got, preludeSentinelEnd) {
		t.Errorf("prelude missing end sentinel: %q", got)
	}
}

func TestBuildTranslationPrelude_EmptyWhenNoTurns(t *testing.T) {
	if got := buildTranslationPrelude("Claude", nil); got != "" {
		t.Errorf("empty entries should return empty prelude, got %q", got)
	}
	onlyTool := []historyEntry{{kind: histPrerendered, text: "just a tool block"}}
	if got := buildTranslationPrelude("Claude", onlyTool); got != "" {
		t.Errorf("tool-only history should return empty prelude, got %q", got)
	}
}

func TestIsTranslationPrelude_MatchesSentinelPrefix(t *testing.T) {
	if !isTranslationPrelude(preludeSentinel + "\nbody") {
		t.Error("should match bare sentinel prefix")
	}
	if !isTranslationPrelude("\n\n" + preludeSentinel + " foo") {
		t.Error("should match after whitespace")
	}
	if isTranslationPrelude("no sentinel here") {
		t.Error("unrelated text should not match")
	}
}

func TestLoadClaudeHistory_SkipsTranslationPrelude(t *testing.T) {
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"<<ASK_TRANSLATION_PRELUDE>>\nprior turns"}}`,
		`{"type":"user","message":{"role":"user","content":"real follow up"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"ok"}]}}`,
	}
	_, id := setupHistoryFixture(t, "pre", strings.Join(lines, "\n"))
	entries, err := loadClaudeHistory(id, HistoryOpts{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Should see only the real follow-up user turn + assistant.
	var userEntries []string
	for _, e := range entries {
		if e.kind == histUser {
			userEntries = append(userEntries, e.text)
		}
	}
	if len(userEntries) != 1 || userEntries[0] != "real follow up" {
		t.Errorf("prelude user entry not skipped; got user entries: %+v", userEntries)
	}
}

func TestSendToProvider_PrependsPreludeAndClears(t *testing.T) {
	fp := newFakeProvider()
	m := newTestModel(t, fp)
	m.proc = &providerProc{}
	m.pendingPrelude = "<<ASK_TRANSLATION_PRELUDE>>\nprior body\n<<END_ASK_TRANSLATION_PRELUDE>>\n"
	m2Tea, _ := m.sendToProvider("hello new provider")
	m2 := m2Tea.(model)
	if len(fp.sentTexts) != 1 {
		t.Fatalf("want 1 send, got %d", len(fp.sentTexts))
	}
	if !strings.Contains(fp.sentTexts[0], "prior body") {
		t.Errorf("prelude not on wire: %q", fp.sentTexts[0])
	}
	if !strings.HasSuffix(fp.sentTexts[0], "hello new provider") {
		t.Errorf("user line not on wire tail: %q", fp.sentTexts[0])
	}
	if m2.pendingPrelude != "" {
		t.Errorf("prelude should be cleared after send: %q", m2.pendingPrelude)
	}
	// Subsequent send must NOT carry the prelude.
	m3Tea, _ := m2.sendToProvider("second turn")
	_ = m3Tea
	if len(fp.sentTexts) != 2 {
		t.Fatalf("want 2 sends, got %d", len(fp.sentTexts))
	}
	if strings.Contains(fp.sentTexts[1], "prior body") {
		t.Errorf("prelude leaked into subsequent send: %q", fp.sentTexts[1])
	}
}

func TestResumeVirtualSession_TranslationPopulatesPrelude(t *testing.T) {
	isolateHome(t)
	claude := newFakeProvider()
	claude.id = "claude"
	claude.displayName = "Claude"
	claude.loadHistoryFn = func(_ string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{
			{kind: histUser, text: "earlier user"},
			{kind: histResponse, text: "earlier assistant"},
		}, nil
	}
	codex := newFakeProvider()
	codex.id = "codex"
	codex.displayName = "Codex"
	withRegisteredProviders(t, claude, codex)

	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "claude", "c-id", "/ws", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	m := newTestModel(t, codex)
	m.cwd = "/ws"
	newM, cmd := m.resumeVirtualSession(sessionEntry{id: vsID, virtualSessionID: vsID})
	mm := newM.(model)
	if mm.pendingTranslationSource != "Claude" {
		t.Errorf("pendingTranslationSource=%q want Claude", mm.pendingTranslationSource)
	}
	if cmd == nil {
		t.Fatal("expected history load cmd")
	}
	hl := cmd().(historyLoadedMsg)
	mm2, _ := runUpdate(t, mm, hl)
	if mm2.pendingPrelude == "" {
		t.Fatal("pendingPrelude must be set after historyLoadedMsg for translation")
	}
	if !strings.Contains(mm2.pendingPrelude, "Claude") {
		t.Errorf("prelude missing source name: %q", mm2.pendingPrelude)
	}
	if !strings.Contains(mm2.pendingPrelude, "earlier user") {
		t.Errorf("prelude missing transcript content: %q", mm2.pendingPrelude)
	}
	if mm2.pendingTranslationSource != "" {
		t.Errorf("pendingTranslationSource should be cleared after prelude built: %q", mm2.pendingTranslationSource)
	}
}

func TestHandleCommand_NewClearsPrelude(t *testing.T) {
	m := newTestModel(t, newFakeProvider())
	m.virtualSessionID = "vs-x"
	m.pendingPrelude = "stale"
	m.pendingTranslationSource = "Stale"
	newM, _ := m.handleCommand("/new")
	mm := newM.(model)
	if mm.virtualSessionID != "" {
		t.Errorf("virtualSessionID not cleared by /new")
	}
	if mm.pendingPrelude != "" || mm.pendingTranslationSource != "" {
		t.Errorf("prelude state not cleared: prelude=%q src=%q", mm.pendingPrelude, mm.pendingTranslationSource)
	}
}

// ---- US-008: Ctrl+B mid-session swap ----

func TestApplyProviderSwitch_CrossProviderWithMappingLoadsHistory(t *testing.T) {
	isolateHome(t)
	pA := newFakeProvider()
	pA.id = "fakeA"
	pA.displayName = "Fake A"
	pB := newFakeProvider()
	pB.id = "fakeB"
	pB.displayName = "Fake B"
	pB.loadHistoryFn = func(id string, _ HistoryOpts) ([]historyEntry, error) {
		return []historyEntry{{kind: histResponse, text: "loaded-from-B:" + id}}, nil
	}
	withRegisteredProviders(t, pA, pB)

	// Seed a VS with mappings for both providers so the swap target has one.
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "fakeA", "nat-A", "/ws", "hi", time.Now().UTC())
	upsertVirtualSession(store, vsID, "/ws", "fakeB", "nat-B", "/ws-B", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	m := newTestModel(t, pA)
	m.virtualSessionID = vsID
	m.sessionID = "nat-A"
	m.cwd = "/ws"
	m.providerSwitchProvIdx = 1 // target B
	newM, cmd := m.applyProviderSwitch("")
	mm := newM.(model)
	if mm.provider.ID() != "fakeB" {
		t.Fatalf("expected provider fakeB, got %s", mm.provider.ID())
	}
	if mm.sessionID != "nat-B" {
		t.Errorf("sessionID=%q want nat-B (mapped from VS)", mm.sessionID)
	}
	if mm.resumeCwd != "/ws-B" {
		t.Errorf("resumeCwd=%q want /ws-B", mm.resumeCwd)
	}
	if mm.pendingPrelude != "" {
		t.Errorf("pendingPrelude should be empty when mapping exists, got %q", mm.pendingPrelude)
	}
	if cmd == nil {
		t.Fatal("expected batched cmd (probe + loadHistory)")
	}
}

func TestApplyProviderSwitch_CrossProviderWithoutMappingPopulatesPrelude(t *testing.T) {
	isolateHome(t)
	pA := newFakeProvider()
	pA.id = "fakeA"
	pA.displayName = "Fake A"
	pB := newFakeProvider()
	pB.id = "fakeB"
	pB.displayName = "Fake B"
	withRegisteredProviders(t, pA, pB)

	// VS only has fakeA mapping; fakeB is the swap target.
	store := &virtualSessionStore{Version: 1}
	vsID := upsertVirtualSession(store, "", "/ws", "fakeA", "nat-A", "/ws", "hi", time.Now().UTC())
	if err := saveVirtualSessions(store); err != nil {
		t.Fatalf("save: %v", err)
	}

	m := newTestModel(t, pA)
	m.virtualSessionID = vsID
	m.cwd = "/ws"
	m.history = []historyEntry{
		{kind: histUser, text: "prior user"},
		{kind: histResponse, text: "prior assistant"},
	}
	m.providerSwitchProvIdx = 1
	newM, _ := m.applyProviderSwitch("")
	mm := newM.(model)
	if mm.sessionID != "" {
		t.Errorf("sessionID=%q want empty (no mapping)", mm.sessionID)
	}
	if mm.pendingPrelude == "" {
		t.Fatal("pendingPrelude must be populated on swap without mapping")
	}
	if !strings.Contains(mm.pendingPrelude, "Fake A") {
		t.Errorf("prelude should cite source Fake A: %q", mm.pendingPrelude)
	}
	if !strings.Contains(mm.pendingPrelude, "prior user") {
		t.Errorf("prelude missing prior turn: %q", mm.pendingPrelude)
	}
	if mm.virtualSessionID != vsID {
		t.Errorf("VS id lost on swap: %q", mm.virtualSessionID)
	}
}

func TestApplyProviderSwitch_SameProviderDoesNotTouchPreludeOrSession(t *testing.T) {
	isolateHome(t)
	p := newFakeProvider()
	p.id = "only"
	withRegisteredProviders(t, p)
	m := newTestModel(t, p)
	m.virtualSessionID = "vs-keep"
	m.sessionID = "keep-session"
	m.resumeCwd = "/keep"
	m.pendingPrelude = "stash"
	m.providerSwitchProvIdx = 0
	newM, _ := m.applyProviderSwitch("new-model")
	mm := newM.(model)
	if mm.sessionID != "keep-session" {
		t.Errorf("same-provider swap dropped sessionID: %q", mm.sessionID)
	}
	if mm.pendingPrelude != "stash" {
		t.Errorf("same-provider swap touched pendingPrelude: %q", mm.pendingPrelude)
	}
	if mm.virtualSessionID != "vs-keep" {
		t.Errorf("VS id dropped: %q", mm.virtualSessionID)
	}
}

// ---- US-009: concurrent-tab write safety ----

func TestMutateVirtualSessions_ConcurrentUpsertsAllPersist(t *testing.T) {
	isolateHome(t)
	const N = 10
	var wg sync.WaitGroup
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := mutateVirtualSessions(func(store *virtualSessionStore) error {
				upsertVirtualSession(store, "", "/ws",
					fmt.Sprintf("prov-%d", i),
					fmt.Sprintf("native-%d", i),
					"/ws",
					fmt.Sprintf("preview %d", i),
					time.Now().UTC())
				return nil
			})
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("mutate failed: %v", err)
	}
	got, err := loadVirtualSessions()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got.Sessions) != N {
		t.Errorf("want %d sessions after concurrent upserts, got %d — locking failed to prevent lost writes",
			N, len(got.Sessions))
	}
}
