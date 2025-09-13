package pipeline

import (
	types "StreamForge/pkg"
	"context"
	"time"

	"go.uber.org/zap"
)

func Retry(ctx context.Context, logger *zap.Logger, retryCfg types.RetryConfig, operation string, fn func() error) error {
	attempts := int32(0)
	interval := time.Duration(retryCfg.InitialIntervalSec * float64(time.Second))

	for {
		select {
		case <-ctx.Done():
			logger.Warn("Retry cancelled", zap.String("operation", operation), zap.Error(ctx.Err()))
			return ctx.Err()
		default:
			err := fn()
			if err == nil {
				return nil
			}
			attempts++
			if attempts >= retryCfg.MaxAttempts {
				logger.Error("Retry limit reached", zap.String("operation", operation), zap.Int32("attempts", attempts), zap.Error(err))
				return err
			}
			logger.Warn("Retry attempt failed", zap.String("operation", operation), zap.Int32("attempt", attempts), zap.Error(err))
			interval = time.Duration(float64(interval) * retryCfg.BackoffCoefficient)
			time.Sleep(interval)
		}
	}
}
