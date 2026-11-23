package api

import (
	"testing"

	"github.com/coroot/coroot/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveInstrumentation_CassandraNoPanic(t *testing.T) {
	app := model.NewApplication(model.NewApplicationId("c1", "default", model.ApplicationKindExternalService, "external-cassandra"))

	// Regression: cassandra has no default instrumentation and previously dereferenced nil.
	assert.NotPanics(t, func() {
		assert.Nil(t, resolveInstrumentation(app, model.ApplicationTypeCassandra))
	})

	// Supported type returns disabled default instrumentation.
	pg := resolveInstrumentation(app, model.ApplicationTypePostgres)
	require.NotNil(t, pg)
	require.NotNil(t, pg.Enabled)
	assert.False(t, *pg.Enabled)
}
