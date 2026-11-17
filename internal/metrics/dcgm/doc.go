// Package dcgm은 dcgm-exporter의 /metrics를 직접 읽는 metrics.MetricSource 구현이다 (개발 단계 7, 설계도 5장).
//
// dcgm-exporter 하나는 노드 한 대의 GPU를 알린다. Source는 엔드포인트마다 /metrics를 받아 Parse로
// 노드별 스냅샷을 만든다. 읽는 값은 설계도 5장 표와 같다.
//   - GPU 카드: 라벨 UUID, gpu, modelName, Hostname
//   - MIG 조각: 라벨 GPU_I_ID, GPU_I_PROFILE (같은 줄의 UUID는 부모 GPU다)
//   - 조각을 쓰는 Pod: 라벨 pod, namespace, container (dcgm-exporter의 Kubernetes 연결을 켰을 때)
//   - 메모리: DCGM_FI_DEV_FB_USED + DCGM_FI_DEV_FB_FREE. 조각 줄은 조각 메모리, GPU 줄은 카드 메모리다
//   - 고장 징후: DCGM_FI_DEV_XID_ERRORS (마지막 XID 코드)
//   - MIG 모드: DCGM_FI_DEV_MIG_MODE. 기본 수집 목록에는 없어 수집 목록에 넣어야 나온다 (설계도 10장 7번)
//
// dcgm-exporter 4.9부터 이름이 바뀐 것도 함께 읽는다: 메모리는 DCGM_FI_DEV_FB_USED_BYTES 같은 바이트 단위,
// XID는 DCGM_FI_DEV_XID_ERROR, 노드 이름 라벨은 hostname, 모델 라벨은 model_name일 수 있다.
//
// MIG 모드를 알려 주는 메트릭이 없으면 조각 줄이 있는 GPU는 MIG가 켜진 것으로, 조각 줄이 없는 GPU는
// 나누지 않은 GPU(조각 하나, 프로파일 full)로 본다. MIG 모드가 켜졌다고 나왔는데 조각이 없으면 조각을 만들지 않는다.
package dcgm
