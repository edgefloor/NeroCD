package hostingbuild

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
)

func (e *Engine) absent(ctx context.Context, dir, name string) (bool, error) {
	b, err := e.docker(ctx, dir, "ps", "--all", "--quiet", "--no-trunc", "--filter", "name=^/"+name+"$")
	return strings.TrimSpace(string(b)) == "", err
}
func (e *Engine) cleanup(ctx context.Context, dir string, r ownedResources) error {
	for _, name := range []string{r.Planner, r.Builder} {
		if err := e.daemon(ctx, dir, r.DaemonID); err != nil {
			return err
		}
		missing, err := e.absent(ctx, dir, name)
		if err != nil {
			return err
		}
		if missing {
			continue
		}
		c, err := e.inspect(ctx, dir, name)
		if err != nil {
			return err
		}
		if !owned(c, r) {
			return errors.New("cleanup refused foreign container")
		}
		if c.State.Running {
			if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
				return err
			}
			if _, err = e.docker(ctx, dir, "stop", "--time", "3", c.ID); err != nil {
				current, e2 := e.inspect(ctx, dir, c.ID)
				if e2 != nil || !owned(current, r) {
					return errors.Join(err, e2)
				}
				if current.State.Running {
					if e2 = e.daemon(ctx, dir, r.DaemonID); e2 != nil {
						return e2
					}
					if _, e2 = e.docker(ctx, dir, "kill", c.ID); e2 != nil {
						return errors.Join(err, e2)
					}
				}
			}
		}
		current, err := e.inspect(ctx, dir, c.ID)
		if err != nil {
			return err
		}
		if !owned(current, r) || current.State.Running {
			return errors.New("container stop unconfirmed")
		}
		if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
			return err
		}
		if _, err = e.docker(ctx, dir, "rm", c.ID); err != nil {
			return err
		}
		missing, err = e.absent(ctx, dir, name)
		if err != nil || !missing {
			return errors.Join(errors.New("container removal unconfirmed"), err)
		}
	}
	if err := e.daemon(ctx, dir, r.DaemonID); err != nil {
		return err
	}
	b, err := e.docker(ctx, dir, "volume", "ls", "--quiet", "--filter", "name=^"+r.Volume+"$")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) == "" {
		return e.cleanupGroup(r)
	}
	b, err = e.docker(ctx, dir, "volume", "inspect", r.Volume)
	if err != nil {
		return err
	}
	var rows []struct {
		Name   string
		Labels map[string]string
	}
	if json.Unmarshal(b, &rows) != nil || len(rows) != 1 || rows[0].Name != r.Volume || rows[0].Labels["io.nerocd.hostingbuild.owner"] != r.Token {
		return errors.New("cleanup refused foreign volume")
	}
	if err = e.daemon(ctx, dir, r.DaemonID); err != nil {
		return err
	}
	_, err = e.docker(ctx, dir, "volume", "rm", r.Volume)
	if err != nil {
		return err
	}
	b, err = e.docker(ctx, dir, "volume", "ls", "--quiet", "--filter", "name=^"+r.Volume+"$")
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(b)) != "" {
		return errors.New("volume removal unconfirmed")
	}
	return e.cleanupGroup(r)
}
