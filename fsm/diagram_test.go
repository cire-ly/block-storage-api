package fsm_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"

	"github.com/looplab/fsm"

	volumefsm "github.com/cire-ly/block-storage-api/fsm"
)

// TestGenerateFSMDiagram generates fsm_diagram.md in the fsm/ directory.
// Run with: go test ./fsm/ -run TestGenerateFSMDiagram -v
func TestGenerateFSMDiagram(t *testing.T) {
	f := volumefsm.NewVolumeFSM(volumefsm.StatePending)

	mermaid, err := fsm.VisualizeWithType(f, fsm.MermaidStateDiagram)
	if err != nil {
		t.Fatalf("VisualizeWithType: %v", err)
	}

	graphviz, err := fsm.VisualizeWithType(f, fsm.GRAPHVIZ)
	if err != nil {
		t.Fatalf("VisualizeWithType graphviz: %v", err)
	}

	out, err := os.Create("fsm_diagram.md")
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	defer out.Close()

	fmt.Fprintln(out, "# Volume FSM — Cycle de vie")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "État initial : `%s`\n\n", volumefsm.StatePending)
	fmt.Fprintln(out, "## États")
	fmt.Fprintln(out)

	states := []struct{ state, desc string }{
		{volumefsm.StatePending, "volume créé, en attente de provisioning"},
		{volumefsm.StateCreating, "provisioning en cours côté backend"},
		{volumefsm.StateAvailable, "prêt à être attaché ou supprimé"},
		{volumefsm.StateAttaching, "attachement en cours vers un nœud"},
		{volumefsm.StateAttached, "attaché à un nœud de calcul"},
		{volumefsm.StateDetaching, "détachement en cours"},
		{volumefsm.StateDeleting, "suppression en cours"},
		{volumefsm.StateDeleted, "supprimé définitivement"},
		{volumefsm.StateError, "erreur irrécupérable"},
	}
	for _, s := range states {
		fmt.Fprintf(out, "- `%s` — %s\n", s.state, s.desc)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "## Transitions")
	fmt.Fprintln(out)

	events := []struct{ event, from, to string }{
		{volumefsm.EventCreate, volumefsm.StatePending, volumefsm.StateCreating},
		{volumefsm.EventReady, volumefsm.StateCreating, volumefsm.StateAvailable},
		{volumefsm.EventError, volumefsm.StateCreating + " | " + volumefsm.StateAttaching + " | " + volumefsm.StateDetaching + " | " + volumefsm.StateDeleting, volumefsm.StateError},
		{volumefsm.EventAttach, volumefsm.StateAvailable, volumefsm.StateAttaching},
		{volumefsm.EventAttached, volumefsm.StateAttaching, volumefsm.StateAttached},
		{volumefsm.EventDetach, volumefsm.StateAttached, volumefsm.StateDetaching},
		{volumefsm.EventDetached, volumefsm.StateDetaching, volumefsm.StateAvailable},
		{volumefsm.EventDelete, volumefsm.StateAvailable, volumefsm.StateDeleting},
		{volumefsm.EventDeleted, volumefsm.StateDeleting, volumefsm.StateDeleted},
	}
	fmt.Fprintln(out, "| Événement | De | Vers |")
	fmt.Fprintln(out, "|-----------|-----|------|")
	for _, e := range events {
		fmt.Fprintf(out, "| `%s` | `%s` | `%s` |\n", e.event, e.from, e.to)
	}

	fmt.Fprintln(out)
	fmt.Fprintln(out, "## Diagramme Mermaid")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "```mermaid")
	fmt.Fprint(out, mermaid)
	fmt.Fprintln(out, "```")

	fmt.Fprintln(out)
	fmt.Fprintln(out, "## Diagramme Graphviz (DOT)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "```dot")
	fmt.Fprint(out, graphviz)
	fmt.Fprintln(out, "```")

	t.Logf("fsm/fsm_diagram.md généré avec succès")
}

// TestGenerateFSMPNG renders the FSM as a PNG via Graphviz dot.
// Skipped automatically if the `dot` binary (graphviz) is not in PATH.
// Install: sudo apt install graphviz  |  brew install graphviz
// Run with: go test ./fsm/ -run TestGenerateFSMPNG -v
func TestGenerateFSMPNG(t *testing.T) {
	dotBin, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("graphviz 'dot' not found in PATH — install with: sudo apt install graphviz")
	}

	f := volumefsm.NewVolumeFSM(volumefsm.StatePending)
	dot, err := fsm.VisualizeWithType(f, fsm.GRAPHVIZ)
	if err != nil {
		t.Fatalf("VisualizeWithType: %v", err)
	}

	var pngBuf bytes.Buffer
	cmd := exec.Command(dotBin, "-Tpng")
	cmd.Stdin = bytes.NewBufferString(dot)
	cmd.Stdout = &pngBuf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("dot -Tpng: %v", err)
	}

	const outPath = "fsm_diagram.png"
	if err := os.WriteFile(outPath, pngBuf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", outPath, err)
	}

	t.Logf("fsm/fsm_diagram.png généré (%d bytes)", pngBuf.Len())
}
