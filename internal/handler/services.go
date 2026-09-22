package handler

import (
	"context"

	"github.com/papersoul/orchestrator/internal/domain"
)

// DocumentService es el contrato que el handler necesita del orquestador.
type DocumentService interface {
	Process(ctx context.Context, in *domain.ProcessInput) (*domain.ProcessResult, error)
}
