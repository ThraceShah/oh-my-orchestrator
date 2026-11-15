package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/domain"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/fleet"
	"github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/httpd/apierr"
	reportsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/report"
	sessionsvc "github.com/oh-my-orchestrator/oh-my-orchestrator/backend/internal/service/session"
)

type fleetWorkerReports interface {
	ListReportsBySession(context.Context, domain.SessionID) ([]domain.ReportRecord, error)
	AcknowledgeFleetReport(context.Context, domain.SessionID, string, time.Time) error
}

// workerSupervision reuses the engine's own session and report service boundary.
// The envelope preserves public error kinds independently of the chat RPC.
func workerSupervision(inner fleet.ChatFunc, sessions *sessionsvc.Service, reports fleetWorkerReports) fleet.ChatFunc {
	return func(ctx context.Context, op, sessionID string, payload json.RawMessage) (json.RawMessage, error) {
		id := domain.SessionID(sessionID)
		reply := func(value any, err error) (json.RawMessage, error) {
			return fleet.EncodeSupervisionReply(value, err), nil
		}
		switch op {
		case "session_get":
			result, err := sessions.Get(ctx, id)
			return reply(result, err)
		case "session_kill":
			result, err := sessions.Kill(ctx, id)
			return reply(result, err)
		case "session_request_kill":
			result, err := sessions.RequestKill(ctx, id)
			return reply(result, err)
		case "session_send":
			var message fleet.SessionMessage
			if err := json.Unmarshal(payload, &message); err != nil {
				return reply(nil, apierr.Invalid("INVALID_MESSAGE", "Invalid session message", nil))
			}
			return reply(nil, sessions.SendWithOptions(ctx, id, message.Message, message.Attachment, message.Options))
		case "session_reports":
			if reports == nil {
				return reply(nil, apierr.NotImplemented("FLEET_REPORTS_UNAVAILABLE", "Worker reports are unavailable"))
			}
			// Unknown sessions must not turn a mistargeted RPC into a successful empty list.
			if _, err := sessions.Get(ctx, id); err != nil {
				return reply(nil, err)
			}
			records, err := reports.ListReportsBySession(ctx, id)
			if err != nil {
				return reply(nil, err)
			}
			out := make([]domain.ReportRecord, 0)
			for _, record := range records {
				if record.DeliveryState != domain.ReportAcknowledged {
					out = append(out, record)
					if len(out) == 100 {
						break
					}
				}
			}
			return reply(out, nil)
		case "session_report_ack":
			if reports == nil {
				return reply(nil, apierr.NotImplemented("FLEET_REPORTS_UNAVAILABLE", "Worker reports are unavailable"))
			}
			var reportID string
			if err := json.Unmarshal(payload, &reportID); err != nil || reportID == "" {
				return reply(nil, apierr.Invalid("INVALID_REPORT_ID", "Report id is required", nil))
			}
			return reply(nil, reports.AcknowledgeFleetReport(ctx, id, reportID, time.Now().UTC()))
		default:
			if inner == nil {
				return nil, apierr.NotImplemented("FLEET_OPERATION_UNSUPPORTED", "Unsupported worker operation")
			}
			return inner(ctx, op, sessionID, payload)
		}
	}
}

// startFleetReportSupervision transfers worker outboxes into a durable control
// inbox, then uses the existing semantic report delivery path to the exact local
// parent coordinator. No report is treated as proof of lifecycle completion.
func startFleetReportSupervision(ctx context.Context, rt *fleet.Runtime, sessions *sessionsvc.Service, local reportSemanticDelivery, log *slog.Logger) {
	if rt == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			cycle, cancel := context.WithTimeout(ctx, 30*time.Second)
			if err := rt.Service.PullReports(cycle); err != nil && cycle.Err() == nil && log != nil {
				log.Warn("fleet report transfer failed", "err", err)
			}
			err := rt.Service.DeliverReports(cycle, func(deliveryContext context.Context, placement fleet.SessionPlacement, report domain.ReportRecord, receipt string) error {
				parent, err := sessions.Get(deliveryContext, domain.SessionID(placement.ParentID))
				if err != nil {
					return err
				}
				if parent.Kind != domain.KindOrchestrator || parent.IsTerminated || parent.ProjectID != report.ProjectID {
					return apierr.Conflict("FLEET_COORDINATOR_UNAVAILABLE", "The report's coordinator is not available", nil)
				}
				batch := reportsvc.PreparedBatch{ProjectID: report.ProjectID, ID: receipt, Reports: []domain.ReportRecord{report}}
				return local.Submit(deliveryContext, parent.ID, batch.Render(), batch.IdempotencyKey())
			})
			if err != nil && log != nil && cycle.Err() == nil {
				log.Warn("fleet report delivery deferred", "err", err)
			}
			cancel()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
