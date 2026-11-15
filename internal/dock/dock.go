// Package dock shows a Dynamic-Island-style floating toolbar (灵动岛) that
// hovers at the top center of the desktop: a small rounded pill hugging the
// top edge by default, expanding into the full toolbar when the mouse
// hovers over it and retracting automatically shortly after the mouse
// leaves. Both the teacher process (quick classroom actions) and the
// student process (raise-hand / message / homework / broadcast-request)
// use it, and it appears automatically when the client starts.
//
// The bar runs its own Win32 message pump on a locked OS thread; all
// mutating calls (SetLabel/SetEnabled/Hide/Show/Close) are safe from any
// goroutine.
package dock

// Button is one toolbar button.
type Button struct {
	ID    int    // caller-assigned; passed back to Options.OnClick
	Label string // short text (2-4 CJK chars recommended)
}

// Options configures the island toolbar.
type Options struct {
	Title   string
	Buttons []Button
	OnClick func(id int) // invoked on the bar's pump thread; keep it quick
	Teacher bool         // teacher style: wider expanded bar, blue theme
}

// MessageBox asks a yes/no question and reports whether the user answered
// yes. Used for destructive quick actions such as shutdown.
