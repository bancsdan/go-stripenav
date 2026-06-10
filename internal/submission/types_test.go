package submission

import "testing"

func TestTransition(t *testing.T) {
	cases := []struct {
		from, to Status
		ok       bool
	}{
		{StatusPending, StatusSubmitted, true},
		{StatusPending, StatusPending, true},
		{StatusPending, StatusRejected, true},
		{StatusPending, StatusAborted, true},
		{StatusPending, StatusAccepted, false},

		// submitted/processing must NOT return to pending: pending means
		// "submit (again)" and these rows already carry a NAV
		// transactionId — requeueing would duplicate the submission.
		// Retriable poll failures back off in place instead.
		{StatusSubmitted, StatusPending, false},
		{StatusSubmitted, StatusProcessing, true},
		{StatusSubmitted, StatusAccepted, true},
		{StatusProcessing, StatusPending, false},
		{StatusProcessing, StatusProcessing, true},
		{StatusProcessing, StatusAccepted, true},

		// Terminal states never transition.
		{StatusAccepted, StatusPending, false},
		{StatusRejected, StatusPending, false},
		{StatusAborted, StatusPending, false},
	}
	for _, c := range cases {
		s := &Submission{Status: c.from}
		err := s.Transition(c.to)
		if c.ok && err != nil {
			t.Errorf("%s → %s: unexpected error %v", c.from, c.to, err)
		}
		if !c.ok && err == nil {
			t.Errorf("%s → %s: expected error, got nil", c.from, c.to)
		}
		if c.ok && s.Status != c.to {
			t.Errorf("%s → %s: status not updated, still %s", c.from, c.to, s.Status)
		}
	}
}

func TestIsTerminal(t *testing.T) {
	for _, st := range []Status{StatusAccepted, StatusRejected, StatusAborted} {
		if !(&Submission{Status: st}).IsTerminal() {
			t.Errorf("%s should be terminal", st)
		}
	}
	for _, st := range []Status{StatusPending, StatusSubmitted, StatusProcessing} {
		if (&Submission{Status: st}).IsTerminal() {
			t.Errorf("%s should not be terminal", st)
		}
	}
}
