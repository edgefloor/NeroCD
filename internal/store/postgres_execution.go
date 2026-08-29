package store

import (
	"context"
	"fmt"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func insertRunLogWithSequence(ctx context.Context, exec sqlcgen.DBTX, log domain.RunLog) (domain.RunLog, error) {
	if log.Sequence < 1 {
		log.Sequence = 1
	}
	row, err := sqlcgen.New(exec).InsertRunLogWithSequence(ctx, sqlcgen.InsertRunLogWithSequenceParams{
		ID: log.ID, RunID: log.RunID, Sequence: int32(log.Sequence), Stream: log.Stream, Message: log.Message, CreatedAt: log.CreatedAt,
	})
	if err != nil {
		return domain.RunLog{}, fmt.Errorf("insert run log query: %w", err)
	}
	return runLogFromSQLC(row), nil
}
