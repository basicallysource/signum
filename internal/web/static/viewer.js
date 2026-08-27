// The part viewer: see the model, see where the mark is. Deliberately not a
// render -- flat neutral material, soft even light, a plain CAD gradient
// behind it -- because the job is reading geometry, not admiring it.
//
// Z stays up, the way the printers and the STLs mean it. The camera fits
// itself to the model's own bounds, and the engraved block is shown as a
// translucent accent quad floating just off its face.

import * as THREE from 'three';
import {OrbitControls} from '/static/vendor/OrbitControls.js';
import {STLLoader} from '/static/vendor/STLLoader.js';

const host = document.getElementById('viewer');
const data = JSON.parse(document.getElementById('viewer-data').textContent);

const scene = new THREE.Scene();

const camera = new THREE.PerspectiveCamera(40, 1, 0.1, 1000);
camera.up.set(0, 0, 1);

const renderer = new THREE.WebGLRenderer({antialias: true, alpha: true});
renderer.setPixelRatio(window.devicePixelRatio || 1);
host.append(renderer.domElement);

// Even, directionless-feeling light: a sky/ground pair does the shaping, a
// faint headlamp keeps down-facing walls readable.
scene.add(new THREE.HemisphereLight(0xffffff, 0x8a8f96, 1.05));
const headlamp = new THREE.DirectionalLight(0xffffff, 0.4);
scene.add(headlamp);

const controls = new OrbitControls(camera, renderer.domElement);
controls.enableDamping = true;
controls.dampingFactor = 0.12;

new STLLoader().load(data.stl, geometry => {
  geometry.computeVertexNormals();
  const part = new THREE.Mesh(geometry, new THREE.MeshStandardMaterial({
    color: 0xb9bec6,
    roughness: 0.95,
    metalness: 0,
    flatShading: true,
  }));
  scene.add(part);

  // Fit: frame the model whatever its size, from a gentle iso angle.
  geometry.computeBoundingSphere();
  const {center, radius} = geometry.boundingSphere;
  const distance = radius / Math.tan((camera.fov * Math.PI) / 360) * 1.25;
  const direction = new THREE.Vector3(1, -1, 0.65).normalize();
  camera.position.copy(center.clone().add(direction.multiplyScalar(distance)));
  camera.near = distance / 100;
  camera.far = distance * 100;
  camera.updateProjectionMatrix();
  controls.target.copy(center);
  controls.update();

  if (data.highlight) {
    scene.add(highlightQuad(data.highlight));
  }
});

// highlightQuad marks the engraved block: its frame arrives from the
// engraver as center + text-direction/up/normal vectors and the block size.
function highlightQuad(h) {
  const u = new THREE.Vector3(...h.u);
  const v = new THREE.Vector3(...h.v);
  const n = new THREE.Vector3(...h.normal);
  const quad = new THREE.Mesh(
    new THREE.PlaneGeometry(h.w * 1.15 + 1, h.h * 1.3 + 1),
    new THREE.MeshBasicMaterial({
      color: 0x3b82f6,
      transparent: true,
      opacity: 0.45,
      side: THREE.DoubleSide,
      depthWrite: false,
    }));
  quad.matrixAutoUpdate = false;
  const m = new THREE.Matrix4().makeBasis(u, v, n);
  m.setPosition(new THREE.Vector3(...h.center).add(n.clone().multiplyScalar(0.15)));
  quad.matrix.copy(m);
  return quad;
}

function resize() {
  const width = host.clientWidth;
  const height = Math.max(280, Math.min(width * 0.62, 520));
  renderer.setSize(width, height);
  camera.aspect = width / height;
  camera.updateProjectionMatrix();
}
new ResizeObserver(resize).observe(host);
resize();

headlamp.position.copy(camera.position);
renderer.setAnimationLoop(() => {
  controls.update();
  headlamp.position.copy(camera.position);
  renderer.render(scene, camera);
});
