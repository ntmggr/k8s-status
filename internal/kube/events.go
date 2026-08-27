package kube

import "context"

// failedSchedulingPath asks the API server for scheduling failures only. The filter is
// applied server-side, so a busy cluster returns a handful of objects rather than every
// event it has.
//
// Events are used rather than pods on purpose. Answering "why is this pending" needs the
// scheduler's own words, and a pod carries its full spec, including environment values
// that are routinely secrets. An Event carries an object reference, a reason and a
// message, and nothing else worth protecting.
const failedSchedulingPath = "/api/v1/events?fieldSelector=reason%3DFailedScheduling"

type EventList struct {
	Items []Event `json:"items"`
}

type Event struct {
	Metadata       EventMetadata  `json:"metadata"`
	InvolvedObject InvolvedObject `json:"involvedObject"`
	Reason         string         `json:"reason"`
	Message        string         `json:"message"`
	LastTimestamp  string         `json:"lastTimestamp"`
}

type EventMetadata struct {
	Namespace string `json:"namespace"`
}

type InvolvedObject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

// ListFailedScheduling returns the scheduler's complaints about pods it could not place.
func (c *Client) ListFailedScheduling(ctx context.Context) (*EventList, error) {
	var list EventList
	if err := c.GetJSON(ctx, FromCache(failedSchedulingPath), "event list", &list); err != nil {
		return nil, err
	}
	return &list, nil
}
