import { describe, it, expect } from 'vitest';
import { problem, health } from './stackHealth';

const stack = (state: string, desired = 'running', containers: any[] = []) => ({
  name: 'x',
  status: { state, containers },
  state: { desired_state: desired },
});

describe('stack health', () => {
  it('a running stack is fine', () => {
    expect(problem(stack('running'))).toBe('');
    expect(health(stack('running'))).toBe('running');
  });

  // Stopped on purpose is not a problem; stopped while meant to run is.
  it('judges stopped by what the operator asked for', () => {
    expect(health(stack('stopped', 'stopped'))).toBe('stopped');
    expect(problem(stack('stopped', 'running'))).toBe('is stopped');
    expect(health(stack('stopped', 'running'))).toBe('problem');
  });

  it('names what is down in a partly running stack', () => {
    const s = stack('partial', 'running', [
      { service: 'web', state: 'running' },
      { service: 'db', state: 'exited' },
    ]);
    expect(problem(s)).toBe('is partly down (db)');
  });

  // restart: always reads "running" between crashes; the daemon's detail says.
  it('catches a crash loop behind a running state', () => {
    const s = stack('running', 'running', [{ service: 'app', state: 'running', detail: 'crash-looping: restarted 4 times' }]);
    expect(problem(s)).toBe('is restarting');
  });

  it('treats a stack with no recorded intent as meant to run', () => {
    expect(problem({ name: 'x', status: { state: 'stopped' } })).toBe('is stopped');
  });
});
