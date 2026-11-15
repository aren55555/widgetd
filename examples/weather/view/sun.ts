// Where the sun is in a place's sky: NOAA's solar position algorithm (the
// one behind its solar calculator, after Meeus, "Astronomical Algorithms"),
// good to a small fraction of a degree for centuries either side of 2000.

const RAD = Math.PI / 180;

/**
 * The geometric altitude the sun's centre has at sunrise and sunset as they
 * are published: its radius (0.27°) plus refraction at the horizon (0.57°).
 */
export const SUNRISE_ALTITUDE = -0.833;

/** The sun's elevation above the horizon, in degrees (negative below it), at a place and a Unix time. */
export function solarElevation(lat: number, lon: number, unixSec: number): number {
  // Julian centuries since J2000.0.
  const T = (unixSec / 86400 + 2440587.5 - 2451545) / 36525;

  const L0 = (280.46646 + T * (36000.76983 + T * 0.0003032)) % 360; // geometric mean longitude
  const M = 357.52911 + T * (35999.05029 - 0.0001537 * T); // mean anomaly
  const e = 0.016708634 - T * (0.000042037 + 0.0000001267 * T); // orbit's eccentricity
  const C =
    Math.sin(M * RAD) * (1.914602 - T * (0.004817 + 0.000014 * T)) +
    Math.sin(2 * M * RAD) * (0.019993 - 0.000101 * T) +
    Math.sin(3 * M * RAD) * 0.000289; // equation of centre
  const omega = 125.04 - 1934.136 * T;
  const lambda = L0 + C - 0.00569 - 0.00478 * Math.sin(omega * RAD); // apparent longitude
  const eps0 = 23 + (26 + (21.448 - T * (46.815 + T * (0.00059 - T * 0.001813))) / 60) / 60;
  const eps = eps0 + 0.00256 * Math.cos(omega * RAD); // obliquity, corrected
  const decl = Math.asin(Math.sin(eps * RAD) * Math.sin(lambda * RAD)); // declination, radians

  // Equation of time, in minutes: how far the sundial runs ahead of the clock.
  const y = Math.tan((eps / 2) * RAD) ** 2;
  const eot =
    (4 / RAD) *
    (y * Math.sin(2 * L0 * RAD) -
      2 * e * Math.sin(M * RAD) +
      4 * e * y * Math.sin(M * RAD) * Math.cos(2 * L0 * RAD) -
      0.5 * y * y * Math.sin(4 * L0 * RAD) -
      1.25 * e * e * Math.sin(2 * M * RAD));

  const utcMin = (((unixSec % 86400) + 86400) % 86400) / 60;
  const solarMin = (((utcMin + eot + 4 * lon) % 1440) + 1440) % 1440; // true solar time
  const hourAngle = (solarMin / 4 - 180) * RAD;

  const cosZenith = Math.sin(lat * RAD) * Math.sin(decl) + Math.cos(lat * RAD) * Math.cos(decl) * Math.cos(hourAngle);
  return 90 - Math.acos(Math.max(-1, Math.min(1, cosZenith))) / RAD;
}

/**
 * How bright the sky is, 0 (night) to 1 (the sun overhead), at an elevation.
 * By day it follows sunlight on level ground, which goes as sin(elevation);
 * through twilight it falls away to nothing once the sun is 18° down, where
 * astronomical twilight ends.
 */
export function skyLight(elevation: number): number {
  const DUSK = 0.4; // the sky's brightness as the sun touches the horizon
  if (elevation >= 0) return DUSK + (1 - DUSK) * Math.sin(elevation * RAD);
  if (elevation <= -18) return 0;
  return DUSK * ((elevation + 18) / 18) ** 2;
}

/** How strongly the horizon glows at dawn and dusk, 0 to 1: most with the sun just under it, gone by 6° either way of that. */
export const horizonGlow = (elevation: number): number => Math.max(0, 1 - Math.abs(elevation + 1) / 7);
