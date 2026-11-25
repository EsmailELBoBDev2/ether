/**
 * Quality mock routes: GET /quality/devices, GET /quality/clients, and
 * POST /quality/explain against the ordered rules model. Development and tests
 * only; the real answer comes from the server's playback code path.
 */
import type {
  DeviceMatch,
  EtherConfig,
  QualityAnswer,
  QualityClient,
  QualityDevices,
  QualityLayer,
  QualityQuestion,
  QualityRule,
} from "../lib/types";

type Reply = { status: number; body: unknown };

const ok = (body: unknown, status = 200): Reply => ({ status, body });
const fail = (status: number, code: string, message: string): Reply => ({
  status,
  body: { error: { code, message } },
});

type Device = {
  maxHeight: number;
  hdr: boolean;
  dolbyVision: boolean;
  group: "tv" | "mobile" | "computer";
  type: string;
  app: string;
};

const CLIENTS: (QualityClient & Device)[] = [
  {
    id: "plex-apple-tv",
    name: "Plex for Apple TV",
    product: "Plex for Apple TV",
    platform: "tvOS",
    device: "Apple TV",
    group: "tv",
    type: "Apple TV",
    app: "plex:Plex for Apple TV",
    maxHeight: 2160,
    hdr: true,
    dolbyVision: true,
  },
  {
    id: "plex-ios",
    name: "Plex for iOS",
    product: "Plex for iOS",
    platform: "iOS",
    device: "iPhone",
    group: "mobile",
    type: "iPhone",
    app: "plex:Plex for iOS",
    maxHeight: 2160,
    hdr: true,
    dolbyVision: true,
  },
  {
    id: "plex-android",
    name: "Plex for Android",
    product: "Plex for Android (Mobile)",
    platform: "Android",
    device: "Pixel 8",
    group: "mobile",
    type: "Android Phone",
    app: "plex:Plex for Android",
    maxHeight: 1080,
    hdr: false,
    dolbyVision: false,
  },
  {
    id: "plex-roku",
    name: "Plex for Roku",
    product: "Plex for Roku",
    platform: "Roku",
    device: "Roku",
    group: "tv",
    type: "Roku",
    app: "plex:Plex for Roku",
    maxHeight: 2160,
    hdr: true,
    dolbyVision: false,
  },
  {
    id: "plex-web-chrome",
    name: "Plex Web (Chrome)",
    product: "Plex Web",
    platform: "Chrome",
    device: "Chrome",
    group: "computer",
    type: "Web Browser",
    app: "plex:Plex Web",
    maxHeight: 2160,
    hdr: false,
    dolbyVision: false,
  },
];

export const MOCK_QUALITY_DEVICES: QualityDevices = {
  groups: [
    { id: "tv", name: "TVs & streaming boxes" },
    { id: "mobile", name: "Phones & tablets" },
    { id: "computer", name: "Computers & browsers" },
  ],
  types: [
    { id: "Apple TV", name: "Apple TV", group: "tv" },
    { id: "NVIDIA Shield", name: "NVIDIA Shield", group: "tv" },
    { id: "Roku", name: "Roku", group: "tv" },
    { id: "Fire TV", name: "Fire TV", group: "tv" },
    { id: "iPhone", name: "iPhone", group: "mobile" },
    { id: "iPad", name: "iPad", group: "mobile" },
    { id: "Android Phone", name: "Android Phone", group: "mobile" },
    { id: "Web Browser", name: "Web Browser", group: "computer" },
  ],
  apps: [
    { id: "plex:Plex for Apple TV", name: "Plex for Apple TV", protocol: "plex" },
    { id: "plex:Plex for Android TV", name: "Plex for Android TV", protocol: "plex" },
    { id: "plex:Plex for iOS", name: "Plex for iOS", protocol: "plex" },
    { id: "plex:Plex for Android", name: "Plex for Android", protocol: "plex" },
    { id: "plex:Plex for Roku", name: "Plex for Roku", protocol: "plex" },
    { id: "plex:Plex Web", name: "Plex Web", protocol: "plex" },
    { id: "jellyfin:Infuse", name: "Infuse", protocol: "jellyfin" },
    { id: "vela:Vela", name: "Vela", protocol: "vela" },
  ],
  clients: [
    {
      id: "client-shield-lr",
      name: "Living room Shield",
      group: "tv",
      type: "NVIDIA Shield",
      app: "plex:Plex for Android TV",
      viewer: "Alex Rivera",
      lastSeen: new Date(Date.now() - 2 * 3600 * 1000).toISOString(),
    },
    {
      id: "client-atv-basement",
      name: "Basement Apple TV",
      group: "tv",
      type: "Apple TV",
      app: "vela:Vela",
      viewer: "Alex Rivera",
      lastSeen: new Date(Date.now() - 24 * 3600 * 1000).toISOString(),
    },
    {
      id: "client-sam-iphone",
      name: "Sam's iPhone",
      group: "mobile",
      type: "iPhone",
      app: "jellyfin:Infuse",
      viewer: "Sam Rivera",
      lastSeen: new Date(Date.now() - 5 * 60 * 1000).toISOString(),
    },
  ],
};

function matchesDevice(m: DeviceMatch, client: QualityClient & Device): boolean {
  if (m.group) return client.group === m.group;
  if (m.type) return client.type === m.type;
  if (m.app) return client.app === m.app;
  if (m.client) return client.id === m.client;
  if (m.contains) {
    const q = m.contains.toLowerCase();
    return (
      client.product.toLowerCase().includes(q) ||
      client.platform.toLowerCase().includes(q) ||
      (client.device ?? "").toLowerCase().includes(q)
    );
  }
  return false;
}

function ruleMatches(r: QualityRule, q: QualityQuestion, client: QualityClient & Device): boolean {
  if (r.where === "home" && q.remote) return false;
  if (r.where === "away" && !q.remote) return false;
  const kind = q.mediaType === "tv" ? "episode" : q.mediaType;
  if (r.kind && kind && r.kind !== kind) return false;
  if (r.viewers?.length) {
    const has = r.viewers.includes(q.userId);
    if (r.exceptViewers ? has : !has) return false;
  }
  if (r.devices?.length) {
    const has = r.devices.some((m) => matchesDevice(m, client));
    if (r.exceptDevices ? has : !has) return false;
  }
  return true;
}

export function createQualityMock() {
  function explain(
    q: QualityQuestion,
    config: EtherConfig,
    policy: Record<string, unknown> | undefined,
  ): Reply {
    const client = CLIENTS.find((c) => c.id === q.clientId);
    if (!client) return fail(400, "unknown_client", "no such app");
    const quality = config.quality;
    const profiles = quality.profiles ?? [];
    const pick = (name: string | undefined) =>
      name ? profiles.find((p) => p.name === name) : undefined;
    const rules = quality.rules ?? [];

    let profileIndex = -1;
    let maxHeightLimit = 0;
    let maxBitrateKbps = 0;
    let heightRuleIndex = -1;
    for (let i = 0; i < rules.length; i++) {
      const r = rules[i]!;
      if (!ruleMatches(r, q, client)) continue;
      if (r.maxHeight && (!maxHeightLimit || r.maxHeight < maxHeightLimit)) {
        maxHeightLimit = r.maxHeight;
        heightRuleIndex = i;
      }
      if (r.maxBitrateKbps && (!maxBitrateKbps || r.maxBitrateKbps < maxBitrateKbps)) {
        maxBitrateKbps = r.maxBitrateKbps;
      }
      if (profileIndex < 0 && r.profile && pick(r.profile)) {
        profileIndex = i;
      }
    }

    const rule = profileIndex >= 0 ? rules[profileIndex] : undefined;
    let source: NonNullable<QualityLayer["source"]> = "first";
    let profile = profiles[0];
    if (rule) {
      const found = pick(rule.profile);
      if (found) {
        source = "rule";
        profile = found;
      }
    } else if (q.remote && quality.defaultAway) {
      const found = pick(quality.defaultAway);
      if (found) {
        source = "remote";
        profile = found;
      }
    } else {
      const found = pick(quality.default);
      if (found) {
        source = "default";
        profile = found;
      }
    }

    const name = profile?.name ?? "default";
    const configured = profile?.maxHeight ?? 1080;
    const ceiling =
      maxHeightLimit && maxHeightLimit < client.maxHeight ? maxHeightLimit : client.maxHeight;
    const maxHeight = configured && (!ceiling || configured <= ceiling) ? configured : ceiling;

    const layers: QualityLayer[] = [
      {
        kind: "profile",
        reason: "",
        profile: name,
        source,
        rule: rule ? profileIndex + 1 : undefined,
        beatsRemote: Boolean(
          rule && q.remote && quality.defaultAway && quality.defaultAway !== name,
        ),
        maxHeight: configured,
        hdr: Boolean(profile?.allowHdr),
        dolbyVision: Boolean(profile?.allowDolbyVision),
        decisive: Boolean(maxHeight && configured === maxHeight),
      },
      {
        kind: "device",
        reason: "",
        maxHeight: client.maxHeight,
        hdr: client.hdr,
        dolbyVision: client.dolbyVision,
        decisive: Boolean(maxHeight && configured !== maxHeight && !maxHeightLimit),
      },
    ];
    if (maxHeightLimit) {
      layers.push({
        kind: "rule-height",
        reason: "",
        rule: heightRuleIndex + 1,
        maxHeight: maxHeightLimit,
        decisive: configured !== maxHeight,
      });
    }
    const blocked = q.remote && Boolean(policy?.remoteDisabled);
    if (blocked) layers.push({ kind: "remote-blocked", reason: "", decisive: true });
    const answer: QualityAnswer = {
      profile: name,
      ...(q.mediaType ? { mediaType: q.mediaType } : {}),
      maxHeight,
      hdr: Boolean(profile?.allowHdr) && client.hdr,
      dolbyVision: Boolean(profile?.allowDolbyVision) && client.dolbyVision,
      bitrateKbps: maxBitrateKbps,
      blocked,
      layers,
    };
    return ok(answer);
  }

  function handle(
    method: string,
    seg: string[],
    body: unknown,
    config: EtherConfig,
    policy: (id: number) => Record<string, unknown> | undefined,
  ): Reply | null {
    if (seg[0] !== "quality") return null;
    const route = `${method} /${seg.join("/")}`;
    if (route === "GET /quality/devices") return ok(MOCK_QUALITY_DEVICES);
    if (route === "GET /quality/clients")
      return ok({
        clients: CLIENTS.map(({ id, name, product, platform, device }) => ({
          id,
          name,
          product,
          platform,
          device,
        })),
      });
    if (route === "POST /quality/explain") {
      const q = (body ?? {}) as QualityQuestion;
      if (!q.userId) return fail(404, "unknown_viewer", "no such viewer");
      return explain(q, config, policy(q.userId));
    }
    return null;
  }

  return { handle };
}
