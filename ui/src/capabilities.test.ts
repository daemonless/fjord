import { describe, it, expect } from 'vitest';
import { detectHostCapabilities, isTruthyYamlBool, normalizePosixPath } from './capabilities';

describe('normalizePosixPath', () => {
  it('resolves . and .. segments cleanly', () => {
    expect(normalizePosixPath('/a/b/../etc')).toBe('/a/etc');
    expect(normalizePosixPath('/containers/../etc')).toBe('/etc');
    expect(normalizePosixPath('/srv/fjord/apps/../../etc')).toBe('/srv/etc');
    expect(normalizePosixPath('/srv/fjord/apps/../../../etc')).toBe('/etc');
    expect(normalizePosixPath('/')).toBe('/');
  });
});

describe('isTruthyYamlBool', () => {
  it('recognizes YAML 1.1 and 1.2 truthy booleans', () => {
    expect(isTruthyYamlBool(true)).toBe(true);
    expect(isTruthyYamlBool('true')).toBe(true);
    expect(isTruthyYamlBool('yes')).toBe(true);
    expect(isTruthyYamlBool('YES')).toBe(true);
    expect(isTruthyYamlBool('on')).toBe(true);
    expect(isTruthyYamlBool('"on"')).toBe(true);
    expect(isTruthyYamlBool(1)).toBe(true);
    expect(isTruthyYamlBool('1')).toBe(true);
    expect(isTruthyYamlBool(false)).toBe(false);
    expect(isTruthyYamlBool('no')).toBe(false);
    expect(isTruthyYamlBool('off')).toBe(false);
  });
});

describe('detectHostCapabilities', () => {
  it('does not flag MOVIES_PATH=/mnt/media/movies when configured in folderSets', () => {
    const compose = `
services:
  radarr:
    image: ghcr.io/daemonless/radarr:latest
    volumes:
      - \${CONFIG_PATH}:/config
      - \${MOVIES_PATH}:/movies
      - \${DOWNLOADS_PATH}:/downloads
      - /etc/localtime:/etc/localtime:ro
      - cache-data:/cache
`;
    expect(
      detectHostCapabilities({
        compose,
        values: {
          CONFIG_PATH: '/srv/fjord/apps/radarr/config',
          MOVIES_PATH: '/mnt/media/movies',
          DOWNLOADS_PATH: '/mnt/scratch/downloads',
        },
        appDataLocations: ['/srv/fjord/apps'],
        folderSets: [{ folders: ['/mnt/media/movies', '/mnt/scratch/downloads'] }],
      }),
    ).toEqual([]);
  });

  it('flags declared variables whose defaults or values resolve outside allowed folders', () => {
    const composeKeys = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${KEYS}:/keys
`;
    expect(
      detectHostCapabilities({
        compose: composeKeys,
        variables: [{ name: 'KEYS', type: 'string', default: '/root/.ssh' }],
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /root/.ssh']);

    const composeSock = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${SOCK_PATH}:/var/run/podman.sock
`;
    expect(
      detectHostCapabilities({
        compose: composeSock,
        variables: [{ name: 'SOCK_PATH', type: 'path', default: '/var/run/podman/podman.sock' }],
        paths: { SOCK_PATH: ['/var/run/podman/podman.sock'] },
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /var/run/podman/podman.sock']);
  });

  it('does not flag custom app data root /srv/fjord/apps with no /containers, and never hardcodes /containers', () => {
    const composeAllowed = `
services:
  radarr:
    image: ghcr.io/daemonless/radarr:latest
    volumes:
      - /srv/fjord/apps/radarr/config:/config
      - \${APP_DATA}/data:/data
      - /etc/localtime:/etc/localtime:ro
`;
    expect(
      detectHostCapabilities({
        compose: composeAllowed,
        values: { APP_DATA: '/srv/fjord/apps/radarr' },
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual([]);

    const composeHardcodedContainers = `
services:
  agent:
    image: ghcr.io/example/agent:latest
    volumes:
      - /containers/agent/config:/config
`;
    expect(
      detectHostCapabilities({
        compose: composeHardcodedContainers,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /containers/agent/config']);
  });

  it('flags path traversal via literal paths, variables, and {{appdata}} placeholders', () => {
    const composeLiteralTraversal = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - /srv/fjord/apps/../etc:/host-etc
`;
    expect(
      detectHostCapabilities({
        compose: composeLiteralTraversal,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /srv/fjord/etc']);

    const composeVarTraversal = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${APP_DATA}/../../etc:/etc
`;
    expect(
      detectHostCapabilities({
        compose: composeVarTraversal,
        values: { APP_DATA: '/srv/apps' },
        appDataLocations: ['/srv/apps'],
      }),
    ).toEqual(['bind mount /etc']);

    const composeSingleDotDotVar = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${APP_DATA}/../etc:/etc
`;
    expect(
      detectHostCapabilities({
        compose: composeSingleDotDotVar,
        values: { APP_DATA: '/srv/fjord/apps/radarr' },
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /srv/fjord/apps/etc']);

    const composePlaceholderTraversal = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - "{{appdata}}/../../../etc:/host-etc"
`;
    expect(
      detectHostCapabilities({
        compose: composePlaceholderTraversal,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /etc']);
  });

  it('flags top-level named volumes using bind driver_opts outside allowed folders', () => {
    const composeNamedBind = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - hostroot:/mnt
volumes:
  hostroot:
    driver: local
    driver_opts:
      type: none
      o: bind
      device: /
`;
    expect(
      detectHostCapabilities({
        compose: composeNamedBind,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /']);
  });

  it('flags network_mode: host, ipc: host, userns_mode: host, and org.freebsd.jail.* annotations', () => {
    const composeNamespacesAndJails = `
services:
  agent:
    image: ghcr.io/example/agent:latest
    network_mode: host
    ipc: host
    userns_mode: host
    annotations:
      org.freebsd.jail.allow.raw_sockets: "true"
      org.freebsd.jail.allow.mlock: "true"
`;
    expect(
      detectHostCapabilities({
        compose: composeNamespacesAndJails,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual([
      'ipc: host',
      'userns_mode: host',
      'network_mode: host',
      'annotations (org.freebsd.jail.allow.raw_sockets)',
    ]);
  });

  it('flags privileged: yes, privileged: "on", and quoted/uppercase pid: host', () => {
    const composeYes = `
services:
  agent:
    image: ghcr.io/example/agent:latest
    privileged: yes
    pid: 'host'
`;
    expect(detectHostCapabilities({ compose: composeYes })).toEqual(['privileged: true', 'pid: host']);

    const composeOn = `
services:
  agent:
    image: ghcr.io/example/agent:latest
    privileged: "on"
`;
    expect(detectHostCapabilities({ compose: composeOn })).toEqual(['privileged: true']);
  });

  it('detects privileged, cap_add, devices, pid: host, and outside bind mounts', () => {
    const compose = `
services:
  agent:
    image: ghcr.io/example/agent:latest
    privileged: true
    pid: host
    cap_add:
      - NET_ADMIN
      - SYS_TIME
    devices:
      - /dev/dri:/dev/dri
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - /etc/localtime:/etc/localtime:ro
`;
    expect(
      detectHostCapabilities({
        compose,
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual([
      'privileged: true',
      'cap_add (NET_ADMIN, SYS_TIME)',
      'devices (/dev/dri)',
      'pid: host',
      'bind mount /var/run/docker.sock',
    ]);
  });

  it('inspects services added by active choices and ignores dropped services', () => {
    const compose = `
services:
  app:
    image: ghcr.io/example/app:latest
  helper:
    image: ghcr.io/example/helper:latest
    privileged: true
`;
    const choices = [
      {
        id: 'mode',
        default: 'safe',
        options: [
          { id: 'safe', drop: ['helper'] },
          {
            id: 'hw',
            drop: ['helper'],
            services: 'worker:\n  image: ghcr.io/example/worker:latest\n  devices:\n    - /dev/dri:/dev/dri\n',
          },
        ],
      },
    ];

    expect(
      detectHostCapabilities({
        compose,
        choices,
        picks: { mode: 'safe' },
      }),
    ).toEqual([]);

    expect(
      detectHostCapabilities({
        compose,
        choices,
        picks: { mode: 'hw' },
      }),
    ).toEqual(['devices (/dev/dri)']);
  });

  it('does not ask for a folder the user chose, only for catalog-supplied ones', () => {
    const lib = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${AUDIOBOOKS_PATH}:/audiobooks
`;
    const variables = [{ name: 'AUDIOBOOKS_PATH', type: 'path', default: '' }];
    const base = { compose: lib, variables, appDataLocations: ['/srv/fjord/apps'], folderSets: [{ folders: ['/mnt/audiobooks'] }] };
    expect(detectHostCapabilities({ ...base, paths: { AUDIOBOOKS_PATH: ['/mnt/media/audiobooks'] } })).toEqual([]);
    expect(detectHostCapabilities({ ...base, formData: { AUDIOBOOKS_PATH: '/mnt/media/audiobooks2' } })).toEqual([]);

    const sock = `
services:
  app:
    image: ghcr.io/example/app:latest
    volumes:
      - \${SOCK}:/var/run/podman.sock
      - \${DATA}/../../etc:/etc
`;
    expect(
      detectHostCapabilities({
        compose: sock,
        variables: [
          { name: 'SOCK', type: 'path', default: '/var/run/podman/podman.sock' },
          { name: 'DATA', type: 'path', default: '' },
        ],
        formData: { SOCK: '/var/run/podman/podman.sock', DATA: '/srv/fjord/apps/x' },
        appDataLocations: ['/srv/fjord/apps'],
      }),
    ).toEqual(['bind mount /var/run/podman/podman.sock', 'bind mount /srv/fjord/etc']);
  });

  it('does not ask for org.freebsd.jail.allow.mlock alone', () => {
    const dotnet = `
services:
  radarr:
    image: ghcr.io/example/radarr:latest
    annotations:
      org.freebsd.jail.allow.mlock: "true"
`;
    expect(detectHostCapabilities({ compose: dotnet, appDataLocations: ['/srv/fjord/apps'] })).toEqual([]);
  });
});
