import { describe, it, expect } from 'vitest';
import { chosenServices, type StackChoice } from './choices';

// immich's choices, as the catalog carries them.
const base = ['immich-server', 'immich-machine-learning', 'redis', 'database'];
const choices: StackChoice[] = [
  {
    id: 'machine_learning',
    default: 'on',
    options: [{ id: 'on' }, { id: 'off', drop: ['immich-machine-learning'] }],
  },
  {
    id: 'public_proxy',
    default: 'off',
    options: [{ id: 'off' }, { id: 'on', services: 'immich-public-proxy:\n  image: x\n' }],
  },
];

describe('chosenServices', () => {
  it('is the compose as it stands with the defaults', () => {
    expect(chosenServices(base, choices, {})).toEqual(base);
  });

  it('leaves out what a picked option drops', () => {
    expect(chosenServices(base, choices, { machine_learning: 'off' })).not.toContain('immich-machine-learning');
  });

  it('adds what a picked option brings', () => {
    expect(chosenServices(base, choices, { public_proxy: 'on' })).toEqual([...base, 'immich-public-proxy']);
  });
});
