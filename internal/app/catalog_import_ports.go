package app

import (
	"context"
	"github.com/MoYuanCN/Jelee/internal/domain"
)

type CatalogImportRepository interface {
	SubmitCatalogImport(context.Context, domain.Actor, string, string, string, []domain.CatalogImportSelection, domain.JobPolicy) (domain.Job, bool, error)
	GetCatalogImportReport(context.Context, domain.Actor, string) (domain.CatalogImportReport, error)
}

type CatalogImportExecutionRepository interface {
	NextCatalogImport(context.Context, domain.JobLease) (domain.CatalogImportTask, error)
	CommitCatalogImport(context.Context, domain.JobLease, domain.CatalogImportTask) error
	FinishCatalogImport(context.Context, domain.JobLease, string, string) error
}
