package main

import (
	"context"
	"testing"
)

func TestEnsureDemoShowcase_NoopWhenDisabledOrUnconfigured(t *testing.T) {
	cfg := demoLoginConfig()
	cfg.DemoLoginEnabled = false
	if err := ensureDemoShowcase(context.Background(), cfg, nil); err != nil {
		t.Fatalf("disabled showcase: %v", err)
	}
	if err := ensureDemoShowcase(context.Background(), demoLoginConfig(), nil); err != nil {
		t.Fatalf("nil store showcase: %v", err)
	}
}

func TestDemoShowcaseUsesIsolatedManagedDocuments(t *testing.T) {
	if demoPublishedDocID == demoPendingDocID || demoPendingDocID == demoConfidentialDocID {
		t.Fatal("showcase documents must be distinct")
	}
	if demoSpaceID == "user-uploads" || demoSpaceID == "" {
		t.Fatal("showcase space must be a managed space, not user-uploads")
	}
}
