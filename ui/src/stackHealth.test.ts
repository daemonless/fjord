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

  // tautulli on army: no container for the whole pull of an up, so it read
  // as stopped-while-meant-to-run and sat under Problems until it finished.
  it('a stack fjord is busy with is not a problem', () => {
    const pulling = { ...stack('stopped', 'running'), busy: 'up' };
    expect(problem(pulling)).toBe('');
    expect(health(pulling)).toBe('running');
    expect(health({ ...stack('running', 'running'), busy: 'down' })).toBe('stopped');
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
