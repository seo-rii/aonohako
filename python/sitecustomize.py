import os


if os.environ.get("IMG_CAPTURE") == "1":
    try:
        import atexit
        import base64
        import io
        import json
        import threading
        import time
        import weakref

        try:
            from PIL import Image

            _RESAMPLE = Image.Resampling.LANCZOS if hasattr(Image, "Resampling") else Image.LANCZOS
        except Exception:
            Image = None
            _RESAMPLE = None

        _OUT_DIR = os.environ.get("IMG_OUT_DIR") or os.path.join(os.getcwd(), "__img__")
        os.makedirs(_OUT_DIR, exist_ok=True)
        _LOG_PATH = os.path.join(_OUT_DIR, "images.jsonl")
        _LOCK = threading.Lock()
        _CAPTURED_FIGURES = weakref.WeakSet()

        def _read_int_env(name, default):
            try:
                return int(os.environ.get(name, default))
            except Exception:
                return default

        _MAX_WIDTH = _read_int_env("IMG_MAX_WIDTH", 1280)
        _MAX_HEIGHT = _read_int_env("IMG_MAX_HEIGHT", 720)
        _WEBP_QUALITY = _read_int_env("IMG_WEBP_QUALITY", 85)
        _DPI = _read_int_env("IMG_DPI", 100)

        def _rasterize_svg(raw):
            try:
                import cairosvg

                return cairosvg.svg2png(bytestring=raw)
            except Exception:
                return None

        def _to_webp(raw):
            if Image is None or _RESAMPLE is None:
                return None
            try:
                img = Image.open(io.BytesIO(raw))
                img.load()
                if _MAX_WIDTH > 0 and _MAX_HEIGHT > 0:
                    img.thumbnail((_MAX_WIDTH, _MAX_HEIGHT), _RESAMPLE)
                if img.mode not in ("RGB", "RGBA"):
                    img = img.convert("RGBA" if "A" in img.mode else "RGB")
                buf = io.BytesIO()
                img.save(buf, format="WEBP", quality=_WEBP_QUALITY, method=6)
                return buf.getvalue()
            except Exception:
                return None

        def _emit_image(mime, raw):
            try:
                if raw is None:
                    return False
                if isinstance(raw, memoryview):
                    raw = raw.tobytes()
                elif isinstance(raw, str):
                    raw = raw.encode("utf-8")
                if not isinstance(raw, (bytes, bytearray)):
                    return False
                raw = bytes(raw)
                if mime == "image/svg+xml":
                    raster = _rasterize_svg(raw)
                    if raster:
                        raw = raster
                        mime = "image/png"
                webp = _to_webp(raw)
                if webp:
                    raw = webp
                    mime = "image/webp"
                payload = {
                    "mime": mime,
                    "b64": base64.b64encode(raw).decode("ascii"),
                    "ts": int(time.time() * 1000),
                }
                line = json.dumps(payload, separators=(",", ":"))
                with _LOCK:
                    with open(_LOG_PATH, "a", encoding="utf-8") as fp:
                        fp.write(line)
                        fp.write("\n")
                return True
            except Exception:
                return False

        def _repr_png_bytes(value):
            if value is None:
                return None
            if isinstance(value, tuple):
                value = value[0]
            if isinstance(value, memoryview):
                return value.tobytes()
            if isinstance(value, bytes):
                return value
            if isinstance(value, bytearray):
                return bytes(value)
            if isinstance(value, str):
                raw = value.strip()
                try:
                    return base64.b64decode(raw, validate=True)
                except Exception:
                    return value.encode("utf-8")
            return None

        def _capture_fig(fig):
            try:
                buf = io.BytesIO()
                fig.savefig(buf, format="png", bbox_inches="tight", dpi=_DPI)
                ok = _emit_image("image/png", buf.getvalue())
                buf.close()
                if ok:
                    try:
                        _CAPTURED_FIGURES.add(fig)
                    except Exception:
                        pass
                return ok
            except Exception:
                return False

        def _capture_pil_image(value):
            if Image is None:
                return False
            try:
                if not isinstance(value, Image.Image):
                    return False
                buf = io.BytesIO()
                value.save(buf, format="PNG")
                ok = _emit_image("image/png", buf.getvalue())
                buf.close()
                return ok
            except Exception:
                return False

        def _capture_renderable(value):
            try:
                if value is None:
                    return False
                if isinstance(value, (list, tuple)):
                    captured = False
                    for item in value[:8]:
                        captured = _capture_renderable(item) or captured
                    return captured
                try:
                    from matplotlib.figure import Figure

                    if isinstance(value, Figure):
                        return _capture_fig(value)
                except Exception:
                    pass
                if hasattr(value, "savefig"):
                    try:
                        buf = io.BytesIO()
                        value.savefig(buf, format="png", bbox_inches="tight", dpi=_DPI)
                        ok = _emit_image("image/png", buf.getvalue())
                        buf.close()
                        return ok
                    except Exception:
                        pass
                if _capture_pil_image(value):
                    return True
                if hasattr(value, "_repr_png_"):
                    data = _repr_png_bytes(value._repr_png_())
                    if data and _emit_image("image/png", data):
                        return True
                if hasattr(value, "_repr_svg_"):
                    data = value._repr_svg_()
                    if data and _emit_image("image/svg+xml", data):
                        return True
            except Exception:
                pass
            return False

        def _patch_matplotlib():
            try:
                import matplotlib

                matplotlib.use("Agg", force=True)
                import matplotlib.pyplot as plt
                from matplotlib.figure import Figure

                def _capture_all_figures(skip_seen):
                    try:
                        figs = [plt.figure(num) for num in plt.get_fignums()]
                        if not figs:
                            return False
                        captured = False
                        for fig in figs:
                            if skip_seen:
                                try:
                                    if fig in _CAPTURED_FIGURES:
                                        continue
                                except Exception:
                                    pass
                            captured = _capture_fig(fig) or captured
                        return captured
                    except Exception:
                        return False

                def _show(*args, **kwargs):
                    try:
                        _capture_all_figures(skip_seen=False)
                        plt.close("all")
                    except Exception:
                        pass
                    return None

                def _fig_show(self, *args, **kwargs):
                    _capture_fig(self)
                    return None

                plt.show = _show
                Figure.show = _fig_show
                atexit.register(lambda: _capture_all_figures(skip_seen=True))
            except Exception:
                pass

        def _patch_ipython_display():
            try:
                from IPython import display as ip_display

                original_display = ip_display.display

                def _display(*objs, **kwargs):
                    for obj in objs:
                        _capture_renderable(obj)
                    try:
                        return original_display(*objs, **kwargs)
                    except Exception:
                        return None

                ip_display.display = _display
            except Exception:
                pass

        def _patch_qiskit():
            try:
                from qiskit import QuantumCircuit

                original_draw = QuantumCircuit.draw
                if not getattr(original_draw, "_aonohako_img_capture", False):

                    def _draw(self, *args, **kwargs):
                        result = original_draw(self, *args, **kwargs)
                        _capture_renderable(result)
                        return result

                    _draw._aonohako_img_capture = True
                    QuantumCircuit.draw = _draw
            except Exception:
                pass

            try:
                import qiskit.visualization as visualization

                original_circuit_drawer = visualization.circuit_drawer
                if not getattr(original_circuit_drawer, "_aonohako_img_capture", False):

                    def _circuit_drawer(*args, **kwargs):
                        result = original_circuit_drawer(*args, **kwargs)
                        _capture_renderable(result)
                        return result

                    _circuit_drawer._aonohako_img_capture = True
                    visualization.circuit_drawer = _circuit_drawer
            except Exception:
                pass

        _patch_matplotlib()
        _patch_ipython_display()
        _patch_qiskit()
    except Exception:
        pass
