
  const scroll = sc ? { left: sc.scrollLeft || 0, top: sc.scrollTop || 0, scrollH: sc.scrollHeight || 0, offset: scrollOffset, w: sc.clientWidth, h: sc.clientHeight, anchorKind: anchorKind }
    : { left: 0, top: 0, scrollH: el.scrollHeight || 0, offset: 0, w: el.clientWidth, h: el.clientHeight, anchorKind: anchorKind };
