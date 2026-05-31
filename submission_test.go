package stripenav

import "testing"

func TestSubmission_Transition(t *testing.T) {
	cases := []struct {
		from, to SubmissionStatus
		ok       bool
	}{
		{StatusPending, StatusSubmitted, true},
		{StatusPending, StatusPending, true},
		{StatusPending, StatusAccepted, false},
		{StatusSubmitted, StatusProcessing, true},
		{StatusSubmitted, StatusAccepted, true},
		{StatusProcessing, StatusAccepted, true},
		{StatusAccepted, StatusPending, false},
		{StatusRejected, StatusPending, false},
		{StatusAborted, StatusAccepted, false},
	}
	for _, c := range cases {
		s := Submission{Status: c.from}
		err := s.Transition(c.to)
		if (err == nil) != c.ok {
			t.Errorf("Transition(%s → %s) err=%v want ok=%v", c.from, c.to, err, c.ok)
		}
	}
}
