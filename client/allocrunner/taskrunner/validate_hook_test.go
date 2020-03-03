package taskrunner

import (
	"testing"

	"github.com/actiontech/dtle/client/config"
	"github.com/actiontech/dtle/client/taskenv"
	"github.com/actiontech/dtle/nomad/structs"
	"github.com/stretchr/testify/require"
)

func TestTaskRunner_Validate_UserEnforcement(t *testing.T) {
	t.Parallel()

	taskEnv := taskenv.NewEmptyBuilder().Build()
	conf := config.DefaultConfig()

	// Try to run as root with exec.
	task := &structs.Task{
		Driver: "exec",
		User:   "root",
	}
	if err := validateTask(task, taskEnv, conf); err == nil {
		t.Fatalf("expected error running as root with exec")
	}

	// Try to run a non-blacklisted user with exec.
	task.User = "foobar"
	require.NoError(t, validateTask(task, taskEnv, conf))

	// Try to run as root with docker.
	task.Driver = "docker"
	task.User = "root"
	require.NoError(t, validateTask(task, taskEnv, conf))
}

func TestTaskRunner_Validate_ServiceName(t *testing.T) {
	t.Parallel()

	builder := taskenv.NewEmptyBuilder()
	conf := config.DefaultConfig()

	// Create a task with a service for validation
	task := &structs.Task{
		Services: []*structs.Service{
			{
				Name: "ok",
			},
		},
	}

	require.NoError(t, validateTask(task, builder.Build(), conf))

	// Add an env var that should validate
	builder.SetHookEnv("test", map[string]string{"FOO": "bar"})
	task.Services[0].Name = "${FOO}"
	require.NoError(t, validateTask(task, builder.Build(), conf))

	// Add an env var that should *not* validate
	builder.SetHookEnv("test", map[string]string{"BAD": "invalid/in/consul"})
	task.Services[0].Name = "${BAD}"
	require.Error(t, validateTask(task, builder.Build(), conf))
}