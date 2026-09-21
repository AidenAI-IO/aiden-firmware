async (input) => {
  const samples = Array.isArray(input) ? input : input.samples;
  const target = Array.isArray(input) ? null : input.target;
  const visibilityGoal = !Array.isArray(input) && input.visibility_goal;
  const physics = await import('/os/androidFling.ts');
  const sleep = ms => new Promise(resolve => setTimeout(resolve, Math.max(0, ms)));
  const held = samples.filter(s => s.down);
  const first = held[0];
  const release = samples.find(s => !s.down && s.time_ms > first.time_ms);
  if (!first || !release) throw new Error('HID trace lacks contact or release');
  const scale = s => ({...s, x:s.x * innerWidth / 1000, y:s.y * innerHeight / 1000});
  const points = [...held, release].map(scale);
  const start = points[0], end = points[points.length - 1];
  const element = document.elementFromPoint(start.x, start.y);
  const container = element?.closest('[data-scroll-container]');
  if (!container) throw new Error('Swipe must start in a scroll container');
  const before = container.scrollTop;
  const beforeX = container.scrollLeft;
  let crossing = null;
  const checkCrossing = phase => {
    if (!target || visibilityGoal) return false;
    const bounds = container.getBoundingClientRect();
    const rows = [...container.querySelectorAll('[data-scroll-lab-item]')];
    const firstVisible = rows.findIndex(row => {
      const box = row.getBoundingClientRect();
      return box.bottom > bounds.top && box.top < bounds.bottom;
    }) + 1;
    if (firstVisible > target) {
      crossing = {phase, target, first_visible: firstVisible, scroll_top: container.scrollTop};
      return true;
    }
    return false;
  };
  const dispatch = (type, p, down) => element.dispatchEvent(new PointerEvent(type, {
    bubbles:true, cancelable:true, pointerId:1, pointerType:'touch',
    isPrimary:true, clientX:p.x, clientY:p.y, button:0, buttons:down ? 1 : 0,
  }));
  dispatch('pointerdown', start, true);
  const began = performance.now();
  for (const point of points.slice(1, -1)) {
    await sleep(point.time_ms - first.time_ms - (performance.now() - began));
    dispatch('pointermove', point, true);
    container.scrollTo({left:beforeX+start.x-point.x, top:before+start.y-point.y, behavior:'auto'});
    if (checkCrossing('contact')) break;
  }
  if (crossing) {
    dispatch('pointercancel', end, false);
  } else {
    await sleep(end.time_ms - first.time_ms - (performance.now() - began));
    dispatch('pointerup', end, false);
  }
  const atRelease = container.scrollTop;
  const releaseX = container.scrollLeft;

  // Same 100 ms finite-window estimator for both revisions. Interpolate the
  // window boundary so a slow tail is measured from reports, never a label.
  const cutoff = Math.max(start.time_ms, end.time_ms - 100);
  let boundary = start;
  for (let i = 1; i < points.length; i++) {
    const a = points[i-1], b = points[i];
    if (b.time_ms >= cutoff && b.time_ms > a.time_ms) {
      const f = (cutoff-a.time_ms)/(b.time_ms-a.time_ms);
      boundary = {x:a.x+(b.x-a.x)*f, y:a.y+(b.y-a.y)*f};
      break;
    }
  }
  const elapsed = end.time_ms-cutoff;
  const vx = elapsed > 0 ? (boundary.x-end.x)*1000/elapsed : 0;
  const vy = elapsed > 0 ? (boundary.y-end.y)*1000/elapsed : 0;
  const speed = Math.hypot(vx, vy);
  const dpr = devicePixelRatio;
  const distance = physics.androidFlingDistance(speed*dpr, physics.ppiForDpr(dpr))/dpr;
  const duration = physics.androidFlingDurationMs(speed*dpr, physics.ppiForDpr(dpr));
  const beganFling = performance.now();
  if (!crossing && speed > 0 && duration > 0) {
    for (;;) {
      const progress = Math.min(1, (performance.now()-beganFling)/duration);
      const covered = distance*physics.androidFlingProgress(progress);
      container.scrollTo({left:releaseX+covered*vx/speed, top:atRelease+covered*vy/speed, behavior:'auto'});
      if (checkCrossing('fling')) break;
      if (progress >= 1) break;
      await sleep(16);
    }
  }
  await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  const bounds = container.getBoundingClientRect();
  const visible = [...container.querySelectorAll('[data-scroll-lab-item]')].map((row,index) => {
    const box = row.getBoundingClientRect();
    return {ordinal:index+1, visible_px:Math.max(0,Math.min(box.bottom,bounds.bottom)-Math.max(box.top,bounds.top))};
  }).filter(row => row.visible_px > 0);
  const targetRow = visible.find(row => row.ordinal === target);
  const visibility = {first:visible[0]?.ordinal, last:visible.at(-1)?.ordinal,
    target_visible:!!targetRow, target_visible_px:targetRow?.visible_px || 0};
  return {before, at_release:atRelease, after:container.scrollTop,
    coast_px:container.scrollTop-atRelease, release_velocity_css_px_s:speed,
    predicted_coast_px:distance, motion_ms:end.time_ms-first.time_ms,
    velocity_window_ms:100, sample_count:points.length, crossing, visibility};
}
