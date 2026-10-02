/*
 * SPDX-FileCopyrightText: mk-go project
 * SPDX-License-Identifier: AGPL-3.0-only
 */
import { expect, it } from 'vitest';
import { rankingParams, resolvedSchedule } from './ranking-query.js';

it('pins stygian pagination to the first resolved schedule even if a new one appears', () => {
	expect(rankingParams('stygian', 0, 0)).not.toHaveProperty('scheduleId');
	const pinned = resolvedSchedule(0, 'stygian', 100);
	expect(rankingParams('stygian', 50, pinned)).toHaveProperty('scheduleId', 100);
	expect(resolvedSchedule(pinned, 'stygian', 101)).toBe(100);
	expect(rankingParams('stygian', 100, pinned)).toHaveProperty('scheduleId', 100);
	expect(resolvedSchedule(pinned, 'spiral', 0)).toBe(0);
});
