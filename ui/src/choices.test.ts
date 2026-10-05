import { describe, it, expect } from 'vitest';
import { chosenServices, choiceVars, type StackChoice } from './choices';

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

// Vikunja's Database choice, as the catalog carries it.
const database: StackChoice = {
  id: 'database',
  default: 'sqlite',
  options: [
    { id: 'sqlite', env: { VIKUNJA_DATABASE_TYPE: 'sqlite' } },
    {
      id: 'mariadb',
      env: { VIKUNJA_DATABASE_TYPE: 'mysql', VIKUNJA_DATABASE_HOST: 'mariadb' },
      defaults: { VIKUNJA_DATABASE_USER: 'vikunja', VIKUNJA_DATABASE_DATABASE: 'vikunja' },
      secrets: ['VIKUNJA_DATABASE_PASSWORD'],
    },
    { id: 'external', ask: [{ name: 'VIKUNJA_DATABASE_TYPE' }, { name: 'VIKUNJA_DATABASE_HOST' }] },
  ],
};

describe('choiceVars', () => {
  it('lists every variable any option sets, fills, makes up or asks', () => {
    expect([...choiceVars([database])].sort()).toEqual([
      'VIKUNJA_DATABASE_DATABASE',
      'VIKUNJA_DATABASE_HOST',
      'VIKUNJA_DATABASE_PASSWORD',
      'VIKUNJA_DATABASE_TYPE',
      'VIKUNJA_DATABASE_USER',
    ]);
  });
  it('is empty without choices', () => {
    expect(choiceVars([]).size).toBe(0);
  });
});
