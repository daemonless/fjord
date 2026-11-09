import { describe, it, expect } from 'vitest';
import { failedAction, problem, health } from './stackHealth';

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

  it('catches a crashed container in a partial stack as restarting', () => {
    const s = stack('partial', 'running', [{ service: 'app', state: 'crashed', detail: 'the app keeps crashing: bad config' }]);
    expect(problem(s)).toBe('is restarting');
  });

  it('treats a stack with no recorded intent as meant to run', () => {
    expect(problem({ name: 'x', status: { state: 'stopped' } })).toBe('is stopped');
  });
});

describe('failedAction', () => {
  const running = { status: { state: 'running' } };
  it('names the action that last ended in an error, until it is busy again', () => {
    const s = { name: 'a', ...running, state: { last_failure: { action: 'update', message: 'the registry has no such version' } } };
    expect(failedAction(s)).toEqual({ label: 'Update failed', verb: 'could not update', message: 'the registry has no such version' });
    expect(problem(s)).toBe('could not update');
    expect(health(s)).toBe('problem');
    expect(failedAction({ ...s, busy: 'update' })).toBeNull();
  });
  it('leaves health failures to the health words, and a stopped stack is still said first', () => {
    expect(failedAction({ name: 'a', ...running, state: { last_failure: { action: 'health' } } })).toBeNull();
    const down = { name: 'a', status: { state: 'stopped' }, state: { last_failure: { action: 'up' } } };
    expect(problem(down)).toBe('is stopped');
    expect(failedAction(down)?.label).toBe('Start failed');
  });
  it('says nothing when nothing failed', () => {
    expect(failedAction({ name: 'a', ...running })).toBeNull();
    expect(problem({ name: 'a', ...running })).toBe('');
  });
});
