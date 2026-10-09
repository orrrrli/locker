// Design tokens: the single place to change color, type and spacing.
// Values come from the approved Claude Design export (round 5, "dirección Editorial").

const rgba = (hex, a = 1) => {
  const n = parseInt(hex.replace('#', ''), 16);
  return { r: ((n >> 16) & 255) / 255, g: ((n >> 8) & 255) / 255, b: (n & 255) / 255, a };
};

// Each color has a value per mode. Screens pick the mode; components never hardcode color.
export const color = {
  bg:       { claro: rgba('#F2F2F7'),      oscuro: rgba('#000000') },
  bg2:      { claro: rgba('#FFFFFF'),      oscuro: rgba('#1C1C1E') },
  label:    { claro: rgba('#000000'),      oscuro: rgba('#FFFFFF') },
  label2:   { claro: rgba('#3C3C43', 0.6), oscuro: rgba('#EBEBF5', 0.6) },
  labelDim: { claro: rgba('#000000', 0.35), oscuro: rgba('#FFFFFF', 0.35) }, // battery edge
  label3:   { claro: rgba('#3C3C43', 0.3), oscuro: rgba('#EBEBF5', 0.3) },
  sep:      { claro: rgba('#3C3C43', 0.29), oscuro: rgba('#545458', 0.6) },
  fill:     { claro: rgba('#787880', 0.16), oscuro: rgba('#787880', 0.24) },
  glass:    { claro: rgba('#FFFFFF', 0.8), oscuro: rgba('#323236', 0.82) },
  accent:   { claro: rgba('#136B3A'),      oscuro: rgba('#3DDC6B') },
  onAccent: { claro: rgba('#FFFFFF'),      oscuro: rgba('#000000') }, // dark: white on #3DDC6B is 1.8:1, black 11.65:1
  onRed:    { claro: rgba('#FFFFFF'),      oscuro: rgba('#FFFFFF') },
  red:      { claro: rgba('#D70015'),      oscuro: rgba('#FF453A') }, // light: accessible systemRed (4.83:1 on #F2F2F7)
  gray:     { claro: rgba('#8E8E93'),      oscuro: rgba('#8E8E93') }, // swipe action (Capitán)
  fillOpaque: { claro: rgba('#DEDEE4'),    oscuro: rgba('#1D1D1F') }, // `fill` pre-blended on bg, for shapes that overlap
  scrim:    { claro: rgba('#000000', 0.22), oscuro: rgba('#000000', 0.5) }, // behind alerts
  knob:     { claro: rgba('#FFFFFF'),      oscuro: rgba('#FFFFFF') },
  orange:   { claro: rgba('#FF9500'),      oscuro: rgba('#FF9F0A') },
  tabSelected: { claro: rgba('#787880', 0.1), oscuro: rgba('#787880', 0.14) },
  island:   { claro: rgba('#000000'),      oscuro: rgba('#000000') },
  homeBar:  { claro: rgba('#000000', 0.25), oscuro: rgba('#FFFFFF', 0.7) },
  // Welcome hero: always on the green pitch, same in both modes.
  hero:       { claro: rgba('#FFFFFF'),       oscuro: rgba('#FFFFFF') },
  hero2:      { claro: rgba('#FFFFFF', 0.78), oscuro: rgba('#FFFFFF', 0.78) },
  heroEyebrow:{ claro: rgba('#FFFFFF', 0.7),  oscuro: rgba('#FFFFFF', 0.7) },
  heroLegal:  { claro: rgba('#FFFFFF', 0.62), oscuro: rgba('#FFFFFF', 0.62) },
  heroFill:   { claro: rgba('#FFFFFF', 0.14), oscuro: rgba('#FFFFFF', 0.14) },
  heroLine:   { claro: rgba('#FFFFFF', 0.07), oscuro: rgba('#FFFFFF', 0.07) },
  heroRing:   { claro: rgba('#0E4628'),       oscuro: rgba('#0E4628') }, // pitch green behind the crests
  appleBg:    { claro: rgba('#FFFFFF'),       oscuro: rgba('#FFFFFF') },
  appleLabel: { claro: rgba('#000000'),       oscuro: rgba('#000000') },
  // Team crest colors (initials avatars on the welcome screen).
  crest1: { claro: rgba('#2E8B57'), oscuro: rgba('#2E8B57') },
  crest2: { claro: rgba('#1C4E80'), oscuro: rgba('#1C4E80') },
  crest3: { claro: rgba('#7A1E2E'), oscuro: rgba('#7A1E2E') },
  crest4: { claro: rgba('#B8860B'), oscuro: rgba('#B8860B') },
  crest5: { claro: rgba('#5B4B8A'), oscuro: rgba('#5B4B8A') },
  // Initials on a crest fill: the kit picks whichever contrasts more with the fill (same in both modes).
  onCrestLight: { claro: rgba('#FFFFFF'), oscuro: rgba('#FFFFFF') },
  onCrestDark:  { claro: rgba('#000000'), oscuro: rgba('#000000') },
};

// Welcome pitch gradient (top → bottom). Gradient stops cannot bind to variables.
export const pitch = { top: rgba('#165E38'), bottom: rgba('#082D1A') };

// Font families. Swapping the look of the app starts here.
export const font = {
  display: 'SF Pro Display', // big numbers, hero
  text: 'SF Pro Display',    // everything else (the export used Display at every size)
  system: 'SF Pro Display',  // OS chrome (status bar, nav). Apple rail: stays SF Pro whatever the brand font is.
};

// Type scale: family role, weight, size, line height, tracking (px).
// Names are the export's, not iOS text styles. Each comment below names the iOS style a size scales with (Dynamic Type).
export const type = {
  hero:        { family: 'display', weight: 'Bold',      size: 40, lh: 44, ls: -1 }, // 40pt, no iOS style: scales with Large Title
  eyebrow:     { family: 'text',    weight: 'Semi Bold', size: 13, lh: 15.5, ls: 0.6, upper: true },
  title3:      { family: 'text',    weight: 'Regular',   size: 20, lh: 24, ls: 0 },
  body:        { family: 'text',    weight: 'Regular',   size: 17, lh: 22, ls: -0.43 },
  bodyStrong:  { family: 'text',    weight: 'Semi Bold', size: 17, lh: 20, ls: 0 },
  callout:     { family: 'text',    weight: 'Regular',   size: 15, lh: 22, ls: -0.43 }, // 15pt = iOS Subheadline (iOS Callout is 16)
  footnote:    { family: 'text',    weight: 'Regular',   size: 15, lh: 20, ls: 0 }, // 15pt = iOS Subheadline (iOS Footnote is 13)
  calloutStrong: { family: 'text',  weight: 'Semi Bold', size: 15, lh: 22, ls: -0.43 },
  avatar:      { family: 'text',    weight: 'Bold',      size: 12, lh: 22, ls: -0.43 },
  statusTime:  { family: 'system',  weight: 'Semi Bold', size: 17, lh: 22, ls: 0 },
  navLabel:    { family: 'system',  weight: 'Regular',   size: 17, lh: 20, ls: 0 },
  // Welcome
  display:     { family: 'display', weight: 'Bold',      size: 52, lh: 54, ls: -1.8 }, // 52pt, no iOS style: scales with Large Title
  heroEyebrow: { family: 'text',    weight: 'Semi Bold', size: 13, lh: 15.5, ls: 0.9, upper: true },
  lead:        { family: 'text',    weight: 'Regular',   size: 17, lh: 23, ls: 0 },
  subhead:     { family: 'text',    weight: 'Regular',   size: 15, lh: 19, ls: 0 },
  crest:       { family: 'text',    weight: 'Bold',      size: 13, lh: 15.5, ls: 0 },
  appleLabel:  { family: 'system',  weight: 'Semi Bold', size: 19, lh: 22.5, ls: 0 }, // 19pt, no iOS style: scales with Headline
  legal:       { family: 'text',    weight: 'Regular',   size: 13, lh: 18, ls: 0 },
  // Forms
  fieldValue:  { family: 'text',    weight: 'Regular',   size: 20, lh: 26, ls: -0.3 },
  secure:      { family: 'text',    weight: 'Regular',   size: 20, lh: 26, ls: 2 },
  link:        { family: 'text',    weight: 'Regular',   size: 15, lh: 18, ls: 0 },
  linkStrong:  { family: 'text',    weight: 'Semi Bold', size: 15, lh: 18, ls: 0 },
  // Lists
  rowTitle:    { family: 'text',    weight: 'Semi Bold', size: 20, lh: 24, ls: -0.4 },
  rowLabel:    { family: 'text',    weight: 'Regular',   size: 17, lh: 20, ls: 0 },
  badge:       { family: 'text',    weight: 'Bold',      size: 15, lh: 18, ls: -0.3 },
  rowStrong:   { family: 'text',    weight: 'Semi Bold', size: 17, lh: 22, ls: -0.43 },
  // Matches
  clock:       { family: 'display', weight: 'Bold',      size: 56, lh: 56, ls: -2 },
  title1:      { family: 'display', weight: 'Bold',      size: 34, lh: 38, ls: -0.8 },
  calloutStrongFlat: { family: 'text', weight: 'Semi Bold', size: 15, lh: 20, ls: 0 },
  profileName: { family: 'display', weight: 'Bold',      size: 28, lh: 32, ls: -0.6 },
  profileInitials: { family: 'text', weight: 'Bold',     size: 18, lh: 21.5, ls: 0 },
  miniInitials:{ family: 'text',    weight: 'Semi Bold', size: 9, lh: 10.5, ls: 0 },
  miniLabel:   { family: 'text',    weight: 'Semi Bold', size: 13, lh: 15.5, ls: 0 },
  caption:     { family: 'text',    weight: 'Regular',   size: 13, lh: 15.5, ls: 0 },
  navStrong:   { family: 'system',  weight: 'Semi Bold', size: 17, lh: 20, ls: 0 },
  noticeTitle: { family: 'text',    weight: 'Semi Bold', size: 17, lh: 22, ls: 0 },
  noticeTitleRead: { family: 'text', weight: 'Regular',  size: 17, lh: 22, ls: 0 },
  noticeTime:  { family: 'text',    weight: 'Regular',   size: 15, lh: 22, ls: 0 },
  fieldTitle:  { family: 'text',    weight: 'Semi Bold', size: 20, lh: 26, ls: -0.4 },
  jersey:      { family: 'text',    weight: 'Semi Bold', size: 20, lh: 22, ls: -0.4, tnum: true }, // numbers and amounts: tabular digits
  captain:     { family: 'text',    weight: 'Bold',      size: 11, lh: 22, ls: -0.43 },
  swipeLabel:  { family: 'system',  weight: 'Regular',   size: 12, lh: 14.5, ls: 0 },
  chipLabel:   { family: 'text',    weight: 'Regular',   size: 15, lh: 18, ls: 0 },
  chipInitials:{ family: 'text',    weight: 'Bold',      size: 11, lh: 13, ls: 0 },
  clockCaption:{ family: 'text',    weight: 'Regular',   size: 20, lh: 24, ls: -0.3 },
  title2:      { family: 'text',    weight: 'Semi Bold', size: 28, lh: 32, ls: -0.6 }, // 28pt = iOS Title 1 (iOS Title 2 is 22)
  venue:       { family: 'text',    weight: 'Regular',   size: 17, lh: 20, ls: -0.2 },
  subheadStrong:{ family: 'text',   weight: 'Semi Bold', size: 15, lh: 19, ls: 0 },
  // Tab bar
  tabLabel:    { family: 'system',  weight: 'Medium',    size: 10, lh: 12, ls: 0.1 },
  tabBadge:    { family: 'system',  weight: 'Semi Bold', size: 12, lh: 14.5, ls: 0 },
};

export const space = { xs: 4, s: 8, sm: 12, m: 14, l: 16, xl: 20, xxl: 28 }; // xxl: gap between sections
export const radius = { pill: 999, card: 22, button: 24, buttonLarge: 26, chip: 16 };
// Presentation device around each screen (not part of the app UI, same in both modes).
export const device = { body: rgba('#1C1C1E'), edge: rgba('#48484A'), edgeWidth: 1.5, bezel: 12, radius: 60, screenRadius: 48,
  shadow: { color: rgba('#000000', 0.18), y: 12, blur: 32 } };

export const size = { screenW: 393, screenH: 852, touch: 44, avatar: 34, row: 52.5, field: 68, buttonLarge: 52, crest: 38, badge: 48, teamRow: 80.5, actionRow: 50.5 };
