package runnerclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"nerocd/internal/domain"
)

type observationResponse[T any] struct {
	value T
	limit int
}

func (r *observationResponse[T]) decodeResponse(body io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(body, int64(r.limit+1)))
	if err != nil {
		return err
	}
	if len(raw) > r.limit {
		return errors.New("observation response exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&r.value); err != nil {
		return err
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("observation response requires one JSON document")
	}
	return nil
}

// HostingObservationTargets requests a bounded server-selected observation sweep.
func (c Client) HostingObservationTargets(ctx context.Context, in domain.HostingObservationTargetsInput) (domain.HostingObservationTargets, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingObservationTargets{}, err
	}
	r := observationResponse[domain.HostingObservationTargets]{limit: 512 << 10}
	err := c.post(ctx, "/api/v1/runners/hosting-observations/targets", in, &r)
	return r.value, err
}

// ReportHostingObservation submits a closed sample and returns its stable acknowledgment.
func (c Client) ReportHostingObservation(ctx context.Context, in domain.HostingRuntimeObservation) (domain.HostingObservationAck, error) {
	if err := in.Validate(); err != nil {
		return domain.HostingObservationAck{}, err
	}
	r := observationResponse[domain.HostingObservationAck]{limit: domain.HostingObservationJSONLimit}
	err := c.post(ctx, "/api/v1/runners/hosting-observations/report", in, &r)
	if err == nil && (r.value.SampleID != in.SampleID || r.value.BodyHash != domain.HostingDocumentHash(in) || r.value.ReceivedAt.IsZero()) {
		return domain.HostingObservationAck{}, errors.New("observation acknowledgment does not match sample")
	}
	return r.value, err
}
