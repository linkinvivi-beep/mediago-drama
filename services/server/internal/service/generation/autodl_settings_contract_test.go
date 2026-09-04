package generation

import (
	"context"

	servicesettings "github.com/mediago-dev/mediago-drama/services/server/internal/service/settings"
)

type validatedAutoDLWorkflowProfileSaver interface {
	SaveValidatedAutoDLWorkflowProfile(context.Context, servicesettings.AutoDLWorkflowProfileMutation) (servicesettings.AutoDLWorkflowProfileResponse, error)
}

var _ validatedAutoDLWorkflowProfileSaver = (*servicesettings.Settings)(nil)
